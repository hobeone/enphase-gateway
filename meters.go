package gateway

import "context"

// MeterConfig describes the configuration of one CT (current transformer)
// installed on the gateway.
type MeterConfig struct {
	EID             int64    `json:"eid"`
	State           string   `json:"state"`           // "enabled" or "disabled"
	MeasurementType string   `json:"measurementType"` // "production", "net-consumption", "total-consumption"
	PhaseMode       string   `json:"phaseMode"`       // "single", "split", "three-phase"
	PhaseCount      int      `json:"phaseCount"`      // Number of phases being monitored
	MeteringStatus  string   `json:"meteringStatus"`  // "normal", "not-metering", etc.
	StatusFlags     []string `json:"statusFlags"`
}

// Meters returns the configuration of all CTs installed on the gateway.
// Call this first to discover which measurement types are available before
// calling MeterReadings or Consumption; a non-metered gateway will have no CTs.
func (c *Client) Meters(ctx context.Context) ([]MeterConfig, error) {
	var out []MeterConfig
	if err := c.doJSON(ctx, "/ivp/meters", &out); err != nil {
		return nil, err
	}
	return out, nil
}
