package gateway

import (
	"bufio"
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

// StreamLiveData enables the gateway's push stream and reads a continuous
// series of LiveData frames, calling fn for each one.
//
// It blocks until one of the following:
//   - ctx is cancelled — returns ctx.Err()
//   - fn returns ErrStopStream — returns nil
//   - fn returns any other error — returns that error
//   - the gateway closes the connection — returns nil
//
// The gateway pushes frames at roughly 1-second intervals. The HTTP connection
// is kept open for the entire duration of the call, so the client's timeout is
// bypassed — context cancellation is the intended way to stop the stream.
//
// Example:
//
//	ctx, cancel := context.WithCancel(context.Background())
//	defer cancel()
//	err := client.StreamLiveData(ctx, func(ld gateway.LiveData) error {
//	    snap := gateway.SnapshotFromLiveData(ld)
//	    fmt.Printf("solar=%.0fW  load=%.0fW\n", snap.SolarW, snap.LoadW)
//	    return nil
//	})
func (c *Client) StreamLiveData(ctx context.Context, fn func(LiveData) error) error {
	if err := c.EnableLiveStream(ctx); err != nil {
		return err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet,
		c.baseURL+"/ivp/livedata/status", nil)
	if err != nil {
		return fmt.Errorf("build stream request /ivp/livedata/status: %w", err)
	}
	req.Header.Set("Accept", "application/json")
	c.mu.RLock()
	jwt := c.jwt
	c.mu.RUnlock()
	if jwt != "" {
		req.Header.Set("Authorization", "Bearer "+jwt)
	}

	// Use a no-timeout client that shares the underlying transport (TLS config,
	// connection pooling). The client-level Timeout would kill a long-lived
	// streaming connection; context cancellation handles termination instead.
	streamClient := &http.Client{Transport: c.httpClient.Transport}

	resp, err := streamClient.Do(req)
	if err != nil {
		return fmt.Errorf("open stream /ivp/livedata/status: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return &Error{StatusCode: resp.StatusCode, Endpoint: "/ivp/livedata/status"}
	}

	scanner := bufio.NewScanner(resp.Body)
	scanner.Buffer(make([]byte, 64*1024), 64*1024)

	for scanner.Scan() {
		line := scanner.Text()

		// Handle both SSE ("data: {...}") and plain NDJSON ("{...}").
		line = strings.TrimPrefix(line, "data: ")

		// Skip empty lines and SSE metadata lines (":heartbeat", "event: ...").
		if line == "" || line[0] != '{' {
			continue
		}

		var frame LiveData
		if err := json.Unmarshal([]byte(line), &frame); err != nil {
			continue // skip malformed frames; don't abort the stream
		}

		if err := fn(frame); err != nil {
			if errors.Is(err, ErrStopStream) {
				return nil
			}
			return err
		}
	}

	if err := scanner.Err(); err != nil {
		// If the context was cancelled the scanner error is a consequence of
		// the body being closed; return the more informative context error.
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return fmt.Errorf("read stream: %w", err)
	}
	return ctx.Err()
}
