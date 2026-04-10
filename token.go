package gateway

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

// ParseExpiry extracts the expiry time from a raw JWT without verifying its
// signature. The gateway JWT is a standard three-part JWT; the exp claim
// lives in the base64url-encoded payload (second part).
func ParseExpiry(rawJWT string) (time.Time, error) {
	parts := strings.Split(rawJWT, ".")
	if len(parts) != 3 {
		return time.Time{}, fmt.Errorf("parse JWT: expected 3 parts, got %d", len(parts))
	}

	decoded, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return time.Time{}, fmt.Errorf("parse JWT payload: %w", err)
	}

	var claims struct {
		Exp int64 `json:"exp"`
	}
	if err := json.Unmarshal(decoded, &claims); err != nil {
		return time.Time{}, fmt.Errorf("parse JWT claims: %w", err)
	}
	if claims.Exp == 0 {
		return time.Time{}, fmt.Errorf("parse JWT: no exp claim found")
	}
	return time.Unix(claims.Exp, 0), nil
}
