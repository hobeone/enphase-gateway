package gateway

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
)

// EnableHighFrequencyMode activates the gateway's high-frequency data collection
// for /ivp/livedata/status by POST-ing {"enable":1} to /ivp/livedata/stream.
//
// Once enabled, the gateway refreshes LiveData readings at roughly 1-second
// intervals instead of the default ~5-second interval. The mode persists until
// the gateway is rebooted; calling EnableHighFrequencyMode when already active
// is safe and returns nil.
//
// After calling this, use LiveData in a polling loop to receive frequent updates:
//
//	if err := client.EnableHighFrequencyMode(ctx); err != nil {
//	    log.Fatal(err)
//	}
//	for {
//	    live, err := client.LiveData(ctx)
//	    // handle err, use live ...
//	    time.Sleep(time.Second)
//	}
func (c *Client) EnableHighFrequencyMode(ctx context.Context) error {
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
