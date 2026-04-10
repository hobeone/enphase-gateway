// Package gateway provides a Go client for the Enphase IQ Gateway local REST API.
//
// The gateway is a local device on your LAN; all endpoints use HTTPS with a
// self-signed certificate. Most endpoints require a Bearer JWT obtained from
// Enphase cloud via FetchJWT.
//
// Quick start:
//
//	jwt, err := gateway.FetchJWT(ctx, username, password, serial)
//	client := gateway.NewClient("envoy.local", jwt.Token)
//	live, err := client.LiveData(ctx)
//	snap := gateway.SnapshotFromLiveData(live)
//	fmt.Printf("Solar: %.0fW  Grid: %.0fW  Load: %.0fW\n",
//	    snap.SolarW, snap.GridW, snap.LoadW)
package gateway

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"
)

const defaultTimeout = 15 * time.Second

// Client communicates with a local Enphase IQ Gateway over HTTPS.
// The gateway uses a self-signed TLS certificate; the client skips verification
// by default, which is required for local-network gateway access.
// Client is safe for concurrent use.
type Client struct {
	baseURL    string
	mu         sync.RWMutex
	jwt        string
	httpClient *http.Client
}

// Option configures a Client at construction time.
type Option func(*Client)

// WithTimeout sets the HTTP client timeout. Default: 15s.
func WithTimeout(d time.Duration) Option {
	return func(c *Client) {
		c.httpClient.Timeout = d
	}
}

// WithHTTPClient replaces the underlying HTTP client entirely.
// Useful for injecting an httptest.Server client in tests.
func WithHTTPClient(hc *http.Client) Option {
	return func(c *Client) {
		c.httpClient = hc
	}
}

// NewClient creates a Client targeting the given gateway address.
// addr may be a bare hostname ("envoy.local", "192.168.1.10") or a full URL
// ("https://envoy.local"). When no scheme is present, HTTPS is assumed.
// The self-signed TLS certificate is accepted automatically.
func NewClient(addr, jwt string, opts ...Option) *Client {
	transport := &http.Transport{
		TLSClientConfig: &tls.Config{InsecureSkipVerify: true}, //nolint:gosec // gateway self-signed cert by design
	}
	c := &Client{
		baseURL: toBaseURL(addr),
		jwt:     jwt,
		httpClient: &http.Client{
			Transport: transport,
			Timeout:   defaultTimeout,
		},
	}
	for _, o := range opts {
		o(c)
	}
	return c
}

// SetJWT updates the JWT used for subsequent requests.
// Call this after fetching a refreshed token. Safe for concurrent use.
func (c *Client) SetJWT(jwt string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.jwt = jwt
}

// maxResponseBytes caps how much of a gateway response body we'll read (1 MiB).
const maxResponseBytes = 1 << 20

// doJSON performs an authenticated GET and JSON-decodes the response into out.
func (c *Client) doJSON(ctx context.Context, path string, out any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+path, nil)
	if err != nil {
		return fmt.Errorf("build request %s: %w", path, err)
	}
	req.Header.Set("Accept", "application/json")
	c.mu.RLock()
	jwt := c.jwt
	c.mu.RUnlock()
	if jwt != "" {
		req.Header.Set("Authorization", "Bearer "+jwt)
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("request %s: %w", path, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return &Error{StatusCode: resp.StatusCode, Endpoint: path}
	}

	if err := json.NewDecoder(io.LimitReader(resp.Body, maxResponseBytes)).Decode(out); err != nil {
		return fmt.Errorf("decode response %s: %w", path, err)
	}
	return nil
}

// toBaseURL returns a base URL for addr, adding "https://" if no scheme is present.
// An existing "http://" scheme is preserved (used in tests with plain httptest servers).
func toBaseURL(addr string) string {
	if strings.HasPrefix(addr, "http://") || strings.HasPrefix(addr, "https://") {
		return addr
	}
	return "https://" + addr
}
