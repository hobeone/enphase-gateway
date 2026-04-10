package gateway

import (
	"errors"
	"fmt"
)

// Error represents an HTTP-level error returned by the IQ Gateway.
type Error struct {
	StatusCode int
	Endpoint   string
}

func (e *Error) Error() string {
	return fmt.Sprintf("gateway: %s returned HTTP %d", e.Endpoint, e.StatusCode)
}

// IsUnauthorized reports whether err is a 401 Unauthorized response.
// This typically means the JWT has expired and a new one must be fetched.
func IsUnauthorized(err error) bool {
	var e *Error
	return errors.As(err, &e) && e.StatusCode == 401
}

// IsNotFound reports whether err is a 404 Not Found response.
// Several endpoints are only present on gateway models with the relevant hardware;
// for example, /ivp/meters/readings requires a metered gateway with CTs installed.
func IsNotFound(err error) bool {
	var e *Error
	return errors.As(err, &e) && e.StatusCode == 404
}
