package gateway

import "context"

// ConsumptionLine is a power measurement for one phase (or the cumulative total).
type ConsumptionLine struct {
	CurrW       float64 `json:"currW"`       // Instantaneous active power (W)
	ActPower    float64 `json:"actPower"`    // Active power (W)
	ApprntPwr   float64 `json:"apprntPwr"`  // Apparent power (VA)
	ReactPwr    float64 `json:"reactPwr"`   // Reactive power (VAr)
	WhDlvdCum   float64 `json:"whDlvdCum"`  // Cumulative energy delivered (Wh)
	WhRcvdCum   float64 `json:"whRcvdCum"`  // Cumulative energy received (Wh)
	VarhLagCum  float64 `json:"varhLagCum"` // Cumulative lagging reactive energy (VArh)
	VarhLeadCum float64 `json:"varhLeadCum"`
	VahCum      float64 `json:"vahCum"`      // Cumulative apparent energy (VAh)
	RMSVoltage  float64 `json:"rmsVoltage"`  // V RMS
	RMSCurrent  float64 `json:"rmsCurrent"`  // A RMS
	PowerFactor float64 `json:"pwrFactor"`
	FreqHz      float64 `json:"freqHz"` // Hz
}

// ConsumptionReport is the response from GET /ivp/meters/reports/consumption.
//
// ReportType clarifies what "consumption" means:
//   - "net-consumption": grid import/export (load minus solar); can be negative when
//     solar exceeds load and excess is exported.
//   - "total-consumption": actual load power, excluding solar contribution.
type ConsumptionReport struct {
	CreatedAt  int64             `json:"createdAt"`  // Unix epoch
	ReportType string            `json:"reportType"` // "net-consumption" or "total-consumption"
	Cumulative ConsumptionLine   `json:"cumulative"` // Sum across all phases
	Lines      []ConsumptionLine `json:"lines"`      // Per-phase breakdown (L1, L2, ...)
}

// Consumption returns power consumption data for home loads.
// Updated every 5 minutes. Requires a consumption CT to be installed
// (metered gateway models only).
//
// Returns IsNotFound if no consumption CT is installed.
func (c *Client) Consumption(ctx context.Context) (ConsumptionReport, error) {
	var out ConsumptionReport
	if err := c.doJSON(ctx, "/ivp/meters/reports/consumption", &out); err != nil {
		return ConsumptionReport{}, err
	}
	return out, nil
}
