package gateway

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
)

// ErrStopStream is a sentinel error that a StreamLiveData callback can return
// to stop iteration cleanly. StreamLiveData returns nil when it sees this value,
// so callers do not need to set up a separate cancel context just to stop early.
var ErrStopStream = errors.New("stop stream")

// EnableLiveStream activates the gateway's push-stream mode for
// /ivp/livedata/status by POST-ing {"enable":1} to /ivp/livedata/stream.
//
// Once enabled, the gateway pushes LiveData frames at roughly 1-second
// intervals on an open HTTP connection. The stream persists until the gateway
// is rebooted; calling EnableLiveStream when the stream is already active is
// safe and returns nil.
//
// Most callers should use StreamLiveData, which calls EnableLiveStream
// automatically. EnableLiveStream is exposed separately for callers that want
// to enable the stream and poll /ivp/livedata/status themselves.
func (c *Client) EnableLiveStream(ctx context.Context) error {
	body, err := json.Marshal(map[string]int{"enable": 1})
	if err != nil {
		return fmt.Errorf("marshal enable payload: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		c.baseURL+"/ivp/livedata/stream", bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("build request /ivp/livedata/stream: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	c.mu.RLock()
	jwt := c.jwt
	c.mu.RUnlock()
	if jwt != "" {
		req.Header.Set("Authorization", "Bearer "+jwt)
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("request /ivp/livedata/stream: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return &Error{StatusCode: resp.StatusCode, Endpoint: "/ivp/livedata/stream"}
	}

	var result struct {
		SCStream string `json:"sc_stream"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, maxResponseBytes)).Decode(&result); err != nil {
		return fmt.Errorf("decode /ivp/livedata/stream response: %w", err)
	}
	if result.SCStream != "enabled" {
		return fmt.Errorf("/ivp/livedata/stream: expected sc_stream=enabled, got %q", result.SCStream)
	}
	return nil
}

// errStreamDisabled is an internal sentinel returned when the first LiveData
// frame reports sc_stream != "enabled". StreamLiveData catches it, POSTs to
// activate the stream via EnableLiveStream, and retries the connection.
var errStreamDisabled = errors.New("stream not enabled")

// StreamLiveData reads LiveData frames from a persistent GET /ivp/livedata/status
// connection, calling fn for each frame received.
//
// On the first frame it checks the Connection.SCStream field. If the gateway
// reports the stream is not yet active, EnableLiveStream is called once to
// activate it and the connection is retried. This avoids unconditional POSTs
// that can disrupt a stream that is already running.
//
// It blocks until one of the following:
//   - the gateway closes the connection — returns nil
//   - ctx is cancelled — returns ctx.Err()
//   - fn returns ErrStopStream — returns nil
//   - fn returns any other error — returns that error
//
// The gateway pushes frames at roughly 1-second intervals once the stream is
// enabled. The HTTP connection stays open for the call's duration; the
// client-level Timeout is bypassed and context cancellation is the intended
// way to stop the stream.
//
// If the gateway closes the connection and you want to keep receiving frames,
// call StreamLiveData again (or use a loop in your own code).
//
// Example:
//
//	ctx, cancel := context.WithCancel(context.Background())
//	defer cancel()
//	for {
//	    err := client.StreamLiveData(ctx, func(ld gateway.LiveData) error {
//	        snap := gateway.SnapshotFromLiveData(ld)
//	        fmt.Printf("solar=%.0fW  load=%.0fW\n", snap.SolarW, snap.LoadW)
//	        return nil
//	    })
//	    if err != nil || ctx.Err() != nil {
//	        break
//	    }
//	}
func (c *Client) StreamLiveData(ctx context.Context, fn func(LiveData) error) error {
	// Use the dedicated stream transport — a completely separate connection pool
	// from the regular API transport. This prevents the gateway from closing the
	// persistent stream GET when concurrent scrape requests arrive on other
	// connections. No timeout is set; context cancellation stops the stream.
	streamClient := &http.Client{Transport: c.streamTransport}

	// enableAttempted guards against an infinite loop: if the stream reports
	// disabled even after we POST to enable it, return the error rather than
	// retrying again.
	enableAttempted := false

	for {
		// Wrap fn to inspect sc_stream on the first frame only.
		streamConfirmed := false
		err := c.readStream(ctx, streamClient, func(ld LiveData) error {
			if !streamConfirmed {
				if ld.Connection.SCStream != "enabled" {
					return errStreamDisabled
				}
				streamConfirmed = true
			}
			return fn(ld)
		})

		switch {
		case errors.Is(err, errStreamDisabled) && !enableAttempted:
			// Stream isn't active yet — POST to enable it once, then retry.
			if enErr := c.EnableLiveStream(ctx); enErr != nil {
				return enErr
			}
			enableAttempted = true
			continue
		case errors.Is(err, ErrStopStream):
			return nil
		case err != nil && ctx.Err() != nil:
			return ctx.Err()
		default:
			return err
		}
	}
}

// readStream opens one GET /ivp/livedata/status and drains it until EOF,
// calling fn for each frame. Returns ErrStopStream if fn requests a stop,
// nil on clean EOF, or an error on any other failure.
func (c *Client) readStream(ctx context.Context, streamClient *http.Client, fn func(LiveData) error) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet,
		c.baseURL+"/ivp/livedata/status", nil)
	if err != nil {
		return fmt.Errorf("build stream request: %w", err)
	}
	req.Header.Set("Accept", "application/json")
	c.mu.RLock()
	jwt := c.jwt
	c.mu.RUnlock()
	if jwt != "" {
		req.Header.Set("Authorization", "Bearer "+jwt)
	}

	resp, err := streamClient.Do(req)
	if err != nil {
		return fmt.Errorf("open stream: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return &Error{StatusCode: resp.StatusCode, Endpoint: "/ivp/livedata/status"}
	}

	// Choose a decoder based on Content-Type:
	//   text/event-stream → SSE lines ("data: {...}\n\n")
	//   anything else     → concatenated JSON objects (json.Decoder)
	//
	// json.Decoder.Decode consumes exactly one JSON value per call regardless
	// of whitespace, making it correct for both plain JSON and NDJSON. The SSE
	// path is kept for gateways that send text/event-stream.
	ct := resp.Header.Get("Content-Type")
	if strings.Contains(ct, "text/event-stream") {
		return readSSE(resp.Body, fn)
	}
	return readJSON(resp.Body, fn)
}

// readJSON parses a stream of concatenated JSON objects using json.Decoder.
// This handles both single-response gateways and persistent chunked streams.
func readJSON(r io.Reader, fn func(LiveData) error) error {
	dec := json.NewDecoder(r)
	for {
		var frame LiveData
		if err := dec.Decode(&frame); err != nil {
			if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
				return nil // clean end-of-stream; caller reconnects if needed
			}
			return fmt.Errorf("decode stream frame: %w", err)
		}
		if err := fn(frame); err != nil {
			return err
		}
	}
}

// readSSE parses a Server-Sent Events stream, extracting JSON payloads from
// "data: {...}" lines and skipping comments and event-type lines.
func readSSE(r io.Reader, fn func(LiveData) error) error {
	buf := make([]byte, 64*1024)
	var acc []byte
	for {
		n, err := r.Read(buf)
		if n > 0 {
			acc = append(acc, buf[:n]...)
			acc = processSSEBuffer(acc, fn)
		}
		if err != nil {
			if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
				return nil
			}
			return fmt.Errorf("read SSE stream: %w", err)
		}
	}
}

// processSSEBuffer scans acc for complete SSE lines, dispatches any "data:"
// lines to fn, and returns the unconsumed tail.
func processSSEBuffer(acc []byte, fn func(LiveData) error) []byte {
	for {
		idx := bytes.IndexByte(acc, '\n')
		if idx < 0 {
			return acc // incomplete line — wait for more data
		}
		line := strings.TrimRight(string(acc[:idx]), "\r")
		acc = acc[idx+1:]

		if line == "" || strings.HasPrefix(line, ":") || strings.HasPrefix(line, "event:") {
			continue
		}
		data := strings.TrimPrefix(line, "data: ")
		if len(data) == 0 || data[0] != '{' {
			continue
		}
		var frame LiveData
		if err := json.Unmarshal([]byte(data), &frame); err != nil {
			continue // skip malformed frames
		}
		if err := fn(frame); err != nil {
			return acc // caller inspects the error via readSSE's return
		}
	}
}
