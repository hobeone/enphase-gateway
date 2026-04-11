package gateway

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"log/slog"
	"net/http"
)

// DebugOption configures the behaviour of WithDebugLogging.
type DebugOption func(*debugTransport)

// WithDebugBodies enables capture and logging of request and response body
// content. Without this option, WithDebugLogging logs only request metadata
// (method, URL, auth presence) and response metadata (status, Content-Type).
//
// The first 4 KiB of each body is captured and logged when the response body
// is closed. For normal JSON endpoints the full response is visible; for the
// streaming connection the initial frames appear at disconnect time.
func WithDebugBodies() DebugOption {
	return func(t *debugTransport) {
		t.logBodies = true
	}
}

// WithDebugLogging wraps the HTTP transport with request/response logging at
// slog.LevelDebug. Each outgoing request logs its method, URL, and whether an
// auth token is present. Each incoming response logs its status code and
// Content-Type. Pass WithDebugBodies() to also log body content.
//
// Authorization header values are never logged; only whether a token is present.
//
// This option is intended for development and troubleshooting. In production,
// omit it or gate it behind a debug flag so that no overhead is paid when
// debug logging is not active.
func WithDebugLogging(opts ...DebugOption) Option {
	return func(c *Client) {
		api := newDebugTransport(c.httpClient.Transport, opts...)
		c.httpClient.Transport = api
		c.streamTransport = newDebugTransport(c.streamTransport, opts...)
	}
}

func newDebugTransport(inner http.RoundTripper, opts ...DebugOption) *debugTransport {
	t := &debugTransport{inner: inner}
	for _, o := range opts {
		o(t)
	}
	return t
}

// debugBodyLimit is the maximum bytes captured per body for logging.
const debugBodyLimit = 4 * 1024

// debugTransport is an http.RoundTripper that logs every gateway request and
// response at slog.LevelDebug. It is a no-op when debug logging is disabled.
type debugTransport struct {
	inner     http.RoundTripper
	logBodies bool
}

func (t *debugTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	if !slog.Default().Enabled(req.Context(), slog.LevelDebug) {
		return t.inner.RoundTrip(req)
	}

	// Capture and restore the request body when body logging is enabled.
	var reqBody string
	if t.logBodies && req.Body != nil && req.Body != http.NoBody {
		data, err := io.ReadAll(io.LimitReader(req.Body, debugBodyLimit))
		req.Body.Close()
		if err == nil {
			req.Body = io.NopCloser(bytes.NewReader(data))
			reqBody = string(data)
		}
	}

	if t.logBodies {
		slog.DebugContext(req.Context(), "gateway → request",
			"method", req.Method,
			"url", req.URL.String(),
			"has_auth", req.Header.Get("Authorization") != "",
			"body", reqBody,
		)
	} else {
		slog.DebugContext(req.Context(), "gateway → request",
			"method", req.Method,
			"url", req.URL.String(),
			"has_auth", req.Header.Get("Authorization") != "",
		)
	}

	resp, err := t.inner.RoundTrip(req)
	if err != nil {
		slog.DebugContext(req.Context(), "gateway ← error",
			"method", req.Method,
			"url", req.URL.String(),
			"error", err,
		)
		return nil, err
	}

	slog.DebugContext(req.Context(), "gateway ← response",
		"method", req.Method,
		"url", req.URL.String(),
		"status", resp.StatusCode,
		"content_type", resp.Header.Get("Content-Type"),
	)

	if t.logBodies {
		resp.Body = &loggingBody{
			inner: resp.Body,
			ctx:   req.Context(),
			label: fmt.Sprintf("gateway ← body %s %s", req.Method, req.URL.Path),
			limit: debugBodyLimit,
		}
	}
	return resp, nil
}

// loggingBody wraps an io.ReadCloser, tee-ing up to limit bytes into an
// internal buffer as the caller reads. When Close is called, the buffered
// content is emitted as a slog.Debug log entry.
type loggingBody struct {
	inner io.ReadCloser
	ctx   context.Context //nolint:containedctx // stored only for slog; not used for cancellation
	buf   bytes.Buffer
	limit int
	label string
}

func (b *loggingBody) Read(p []byte) (int, error) {
	n, err := b.inner.Read(p)
	if n > 0 && b.buf.Len() < b.limit {
		space := b.limit - b.buf.Len()
		if n <= space {
			b.buf.Write(p[:n])
		} else {
			b.buf.Write(p[:space])
		}
	}
	return n, err
}

func (b *loggingBody) Close() error {
	err := b.inner.Close()
	slog.DebugContext(b.ctx, b.label,
		"bytes_captured", b.buf.Len(),
		"truncated", b.buf.Len() >= b.limit,
		"body", b.buf.String(),
	)
	return err
}
