package gateway

import "context"

// GridPhase contains electrical measurements at one phase of the grid
// connection point.
//
// Sign convention (from Enphase spec):
//   - Negative ActivePower: exporting to grid (surplus solar or battery discharge)
//   - Positive ActivePower: importing from grid (demand exceeds local generation)
type GridPhase struct {
	Phase         string  `json:"phase"`         // "L1", "L2", "L3"
	ActivePower   float64 `json:"activePower"`   // W
	ReactivePower float64 `json:"reactivePower"` // VAr
	Voltage       float64 `json:"voltage"`       // V RMS
	Current       float64 `json:"current"`       // A RMS
	Freq          float64 `json:"freq"`          // Hz
}

// GridReading wraps a set of per-phase grid measurements.
// The gateway returns a single-element slice in normal operation.
type GridReading struct {
	Channels []GridPhase `json:"channels"`
}

// GridReadings returns voltage, current, frequency, and active/reactive power
// at each phase of the grid connection point.
//
// Negative ActivePower on any channel means the system is exporting on that phase.
// For a per-phase view of grid imports/exports, sum Channels[*].ActivePower.
func (c *Client) GridReadings(ctx context.Context) ([]GridReading, error) {
	var out []GridReading
	if err := c.doJSON(ctx, "/ivp/meters/gridReading", &out); err != nil {
		return nil, err
	}
	return out, nil
}
