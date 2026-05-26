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

// noCopy prevents Client values from being copied after first use.
// Embedding it causes go vet to report an error if a Client is copied
// (e.g. passed by value or assigned with *). This is the same technique
// used by sync.Mutex, sync.WaitGroup, and strings.Builder.
type noCopy struct{}

func (*noCopy) Lock()   {}
func (*noCopy) Unlock() {}

// Client communicates with a local Enphase IQ Gateway over HTTPS.
// The gateway uses a self-signed TLS certificate; the client skips verification
// by default, which is required for local-network gateway access.
// Client is safe for concurrent use; do not copy after first use.
type Client struct {
	noCopy             noCopy //nolint:unused
	baseURL            string
	mu                 sync.RWMutex
	jwt                string
	httpClient         *http.Client
	insecureSkipVerify bool
	meterTypesMu       sync.Mutex
	meterTypes         map[int64]string // EID -> MeasurementType; nil until loaded
}

// Gateway is the interface implemented by Client. Embed or accept this
// interface in your own types so you can substitute a test double in
// unit tests without hitting a real IQ Gateway.
type Gateway interface {
	LiveData(ctx context.Context) (LiveData, error)
	MeterReadings(ctx context.Context) ([]CTReading, error)
	TypedMeterReadings(ctx context.Context) ([]TypedCTReading, error)
	GridReadings(ctx context.Context) ([]GridReading, error)
	Meters(ctx context.Context) ([]MeterConfig, error)
	Consumption(ctx context.Context) (ConsumptionReport, error)
	Production(ctx context.Context) (ProductionData, error)
	Inverters(ctx context.Context) ([]InverterReading, error)
	Energy(ctx context.Context) (EnergyData, error)
	Devices(ctx context.Context) (DeviceList, error)
	BatteryInventory(ctx context.Context) ([]BatteryStatus, error)
	SystemInfo(ctx context.Context) (SystemInfo, error)
	EnableHighFrequencyMode(ctx context.Context) error
	SetJWT(jwt string)
}

// Option configures a Client at construction time.
type Option func(*Client)

// WithTimeout sets the HTTP client timeout. Default: 15s.
func WithTimeout(d time.Duration) Option {
	return func(c *Client) {
		if c.httpClient == nil {
			c.httpClient = &http.Client{Timeout: d}
		} else {
			c.httpClient.Timeout = d
		}
	}
}

// WithHTTPClient replaces the underlying HTTP client entirely.
// Useful for injecting an httptest.Server client in tests.
func WithHTTPClient(hc *http.Client) Option {
	return func(c *Client) {
		c.httpClient = hc
	}
}

// WithInsecureSkipVerify sets whether the HTTP client skips TLS certificate verification.
func WithInsecureSkipVerify(skip bool) Option {
	return func(c *Client) {
		c.insecureSkipVerify = skip
	}
}

// newTLSTransport returns a fresh *http.Transport that configures TLS verification.
// Each call produces an independent transport with its own connection pool.
func newTLSTransport(insecure bool) *http.Transport {
	return &http.Transport{
		TLSClientConfig: &tls.Config{InsecureSkipVerify: insecure}, //nolint:gosec
	}
}

// NewClient creates a Client targeting the given gateway address.
// addr may be a bare hostname ("envoy.local", "192.168.1.10") or a full URL
// ("https://envoy.local"). When no scheme is present, HTTPS is assumed.
// The self-signed TLS certificate is accepted automatically if insecure is true.
func NewClient(addr, jwt string, opts ...Option) *Client {
	c := &Client{
		baseURL:            toBaseURL(addr),
		jwt:                jwt,
		insecureSkipVerify: true, // Maintain backwards compatibility default of skipping TLS verify
	}
	for _, o := range opts {
		o(c)
	}
	if c.httpClient == nil {
		c.httpClient = &http.Client{
			Transport: newTLSTransport(c.insecureSkipVerify),
			Timeout:   defaultTimeout,
		}
	}
	return c
}

// TypedMeterReadings returns instantaneous readings from all installed CTs
// mapped to their respective measurement types using the gateway's meter configuration.
// It caches the meter types mapping on first successful call.
func (c *Client) TypedMeterReadings(ctx context.Context) ([]TypedCTReading, error) {
	c.meterTypesMu.Lock()
	if c.meterTypes == nil {
		c.meterTypesMu.Unlock()
		meters, err := c.Meters(ctx)
		if err != nil {
			return nil, fmt.Errorf("fetch meter config: %w", err)
		}
		temp := make(map[int64]string, len(meters))
		for _, m := range meters {
			temp[m.EID] = m.MeasurementType
		}
		c.meterTypesMu.Lock()
		if c.meterTypes == nil {
			c.meterTypes = temp
		}
	}
	meterTypes := c.meterTypes
	c.meterTypesMu.Unlock()

	readings, err := c.MeterReadings(ctx)
	if err != nil {
		return nil, err
	}

	result := make([]TypedCTReading, len(readings))
	for i, r := range readings {
		result[i] = TypedCTReading{
			CTReading:       r,
			MeasurementType: meterTypes[r.EID],
		}
	}
	return result, nil
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
	defer func() { _ = resp.Body.Close() }()

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
