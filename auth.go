package gateway

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"strings"
	"time"
)

// EnlightenBaseURL is the Enphase cloud login endpoint.
// Overridable in tests.
var EnlightenBaseURL = "https://enlighten.enphaseenergy.com"

// EntrezBaseURL is the Enphase token issuance endpoint.
// Overridable in tests.
var EntrezBaseURL = "https://entrez.enphaseenergy.com"

// TokenResponse is the result of a successful JWT fetch.
type TokenResponse struct {
	Token     string `json:"token"`
	ExpiresAt int64  `json:"expires_at"` // Unix epoch; may be zero if not returned by the server
}

// Expiry converts ExpiresAt to a time.Time.
// Returns zero time if ExpiresAt was not populated.
func (t TokenResponse) Expiry() time.Time {
	if t.ExpiresAt == 0 {
		return time.Time{}
	}
	return time.Unix(t.ExpiresAt, 0)
}

// FetchJWT obtains a gateway JWT from Enphase cloud using owner credentials.
// It implements the two-step flow from the IQ Gateway API spec (section 3.3):
//  1. POST /login/login.json to obtain a session_id
//  2. POST entrez.enphaseenergy.com/tokens to exchange session_id for a JWT
//
// The returned JWT is valid for one year when using system owner credentials.
// serial is the IQ Gateway serial number (visible in the Enphase app under
// System > Devices > Gateway).
func FetchJWT(ctx context.Context, username, password, serial string) (TokenResponse, error) {
	jar, err := cookiejar.New(nil)
	if err != nil {
		return TokenResponse{}, fmt.Errorf("create cookie jar: %w", err)
	}
	client := &http.Client{Jar: jar, Timeout: 30 * time.Second}

	// Step 1: authenticate to get a session_id.
	loginBody, err := doPost(ctx, client,
		EnlightenBaseURL+"/login/login.json",
		"application/x-www-form-urlencoded",
		strings.NewReader(url.Values{
			"user[email]":    {username},
			"user[password]": {password},
		}.Encode()),
	)
	if err != nil {
		return TokenResponse{}, fmt.Errorf("enphase login: %w", err)
	}

	var loginData struct {
		SessionID string `json:"session_id"`
	}
	if err := json.Unmarshal(loginBody, &loginData); err != nil || loginData.SessionID == "" {
		return TokenResponse{}, fmt.Errorf("enphase login: no session_id in response")
	}

	// Step 2: exchange session_id for a gateway JWT.
	tokenPayload, err := json.Marshal(map[string]string{
		"session_id": loginData.SessionID,
		"serial_num": serial,
		"username":   username,
	})
	if err != nil {
		return TokenResponse{}, fmt.Errorf("marshal token request: %w", err)
	}
	raw, err := doPost(ctx, client,
		EntrezBaseURL+"/tokens",
		"application/json",
		bytes.NewReader(tokenPayload),
	)
	if err != nil {
		return TokenResponse{}, fmt.Errorf("fetch token: %w", err)
	}

	return parseTokenResponse(raw)
}

// parseTokenResponse handles both JSON {"token":"..."} and raw JWT string responses.
// The token endpoint documentation shows a raw string, but we accept JSON for
// future-proofing.
func parseTokenResponse(raw []byte) (TokenResponse, error) {
	trimmed := strings.TrimSpace(string(raw))

	if strings.HasPrefix(trimmed, "{") {
		var tr TokenResponse
		if err := json.Unmarshal(raw, &tr); err != nil {
			return TokenResponse{}, fmt.Errorf("decode token response: %w", err)
		}
		// Populate expiry from the JWT itself if the server didn't include it.
		if tr.ExpiresAt == 0 {
			if exp, err := ParseExpiry(tr.Token); err == nil {
				tr.ExpiresAt = exp.Unix()
			}
		}
		return tr, nil
	}

	// Plain JWT string.
	tr := TokenResponse{Token: trimmed}
	if exp, err := ParseExpiry(tr.Token); err == nil {
		tr.ExpiresAt = exp.Unix()
	}
	return tr, nil
}

func doPost(ctx context.Context, client *http.Client, endpoint, contentType string, body io.Reader) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, body)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", contentType)

	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("%s: HTTP %s", endpoint, resp.Status)
	}
	return io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes))
}
