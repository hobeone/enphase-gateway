package gateway

import (
	"context"
	"encoding/xml"
	"fmt"
	"io"
	"net/http"
)

// SystemInfo contains hardware and firmware identification from the gateway.
// This is the only endpoint that returns XML and the only one that does not
// require a JWT.
type SystemInfo struct {
	XMLName xml.Name `xml:"envoy_info"`
	Time    int64    `xml:"time"`
	Device  struct {
		SerialNumber string `xml:"sn"`
		PartNumber   string `xml:"pn"`
		Software     string `xml:"software"` // e.g. "D7.4.22"
		IsMeter      bool   `xml:"imeter"`
	} `xml:"device"`
	WebTokens bool `xml:"web-tokens"`
	Packages  []struct {
		Name    string `xml:"name,attr"`
		Version string `xml:"version"`
		Build   string `xml:"build"`
	} `xml:"package"`
}

// SystemInfo fetches hardware identification and firmware version from the gateway.
// This endpoint does not require authentication and returns XML (unlike all others).
// It is useful as a connectivity check and for discovering the gateway's serial number.
func (c *Client) SystemInfo(ctx context.Context) (SystemInfo, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+"/info", nil)
	if err != nil {
		return SystemInfo{}, fmt.Errorf("build request /info: %w", err)
	}
	// No JWT needed; no Accept: application/json — this endpoint speaks XML.

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return SystemInfo{}, fmt.Errorf("request /info: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return SystemInfo{}, &Error{StatusCode: resp.StatusCode, Endpoint: "/info"}
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes))
	if err != nil {
		return SystemInfo{}, fmt.Errorf("read /info: %w", err)
	}

	var info SystemInfo
	if err := xml.Unmarshal(body, &info); err != nil {
		return SystemInfo{}, fmt.Errorf("decode /info: %w", err)
	}
	return info, nil
}
