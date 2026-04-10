package gateway

import "context"

// CTChannel holds a single-phase measurement from a current transformer.
// All energy values are cumulative totals; power values are instantaneous.
type CTChannel struct {
	EID                int64   `json:"eid"`
	Timestamp          float64 `json:"timestamp"`          // Unix epoch (gateway returns float)
	ActEnergyDelivered float64 `json:"actEnergyDlvd"`      // Wh cumulative (energy out)
	ActEnergyReceived  float64 `json:"actEnergyRcvd"`      // Wh cumulative (energy in)
	ApparentEnergy     float64 `json:"apparentEnergy"`     // VAh cumulative
	ReactEnergyLagg    float64 `json:"reactEnergyLagg"`    // VArh cumulative (lagging)
	ReactEnergyLead    float64 `json:"reactEnergyLead"`    // VArh cumulative (leading)
	InstantDemand      float64 `json:"instantaneousDemand"` // W instantaneous
	ActivePower        float64 `json:"activePower"`         // W instantaneous
	ApparentPower      float64 `json:"apparentPower"`       // VA instantaneous
	ReactivePower      float64 `json:"reactivePower"`       // VAr instantaneous
	PowerFactor        float64 `json:"pwrFactor"`
	Voltage            float64 `json:"voltage"` // V RMS
	Current            float64 `json:"current"` // A RMS
	Freq               float64 `json:"freq"`    // Hz
}

// CTReading is the aggregate reading for one installed CT, plus a per-phase
// channel breakdown. The gateway reports one CTReading per installed CT:
// typically production (eid ~704643328), storage, and net-consumption.
type CTReading struct {
	CTChannel
	Channels []CTChannel `json:"channels"`
}

// MeterReadings returns instantaneous readings from all installed CTs,
// including production, storage, and consumption transformers.
// Data is refreshed every 5 minutes by the gateway firmware.
//
// Returns IsNotFound if no CTs are installed (standard non-metered gateways).
func (c *Client) MeterReadings(ctx context.Context) ([]CTReading, error) {
	var out []CTReading
	if err := c.doJSON(ctx, "/ivp/meters/readings", &out); err != nil {
		return nil, err
	}
	return out, nil
}
