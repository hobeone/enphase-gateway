package gateway

import "context"

// ProductionData contains cumulative energy totals and current output
// from the production meter.
type ProductionData struct {
	WattHoursToday     int `json:"wattHoursToday"`     // Wh since midnight local time
	WattHoursSevenDays int `json:"wattHoursSevenDays"` // Wh over the last 7 days
	WattHoursLifetime  int `json:"wattHoursLifetime"`  // Wh total lifetime production
	WattsNow           int `json:"wattsNow"`           // Current active power (W)
}

// InverterReading is the last-reported power output of a single microinverter.
// Updated every 5 minutes by the gateway firmware.
type InverterReading struct {
	SerialNumber    string `json:"serialNumber"`
	LastReportDate  int64  `json:"lastReportDate"`  // Unix epoch of last report
	DevType         int    `json:"devType"`
	LastReportWatts int    `json:"lastReportWatts"` // W at last report
	MaxReportWatts  int    `json:"maxReportWatts"`  // W all-time peak
}

// Production returns aggregate energy totals and current production power.
// This endpoint works even without a production CT installed (it falls back
// to summing microinverter reports).
func (c *Client) Production(ctx context.Context) (ProductionData, error) {
	var out ProductionData
	if err := c.doJSON(ctx, "/api/v1/production", &out); err != nil {
		return ProductionData{}, err
	}
	return out, nil
}

// Inverters returns per-microinverter production data for all panels.
// Updated every 5 minutes. Use this to identify under-performing panels
// (compare LastReportWatts against MaxReportWatts and neighbours).
func (c *Client) Inverters(ctx context.Context) ([]InverterReading, error) {
	var out []InverterReading
	if err := c.doJSON(ctx, "/api/v1/production/inverters", &out); err != nil {
		return nil, err
	}
	return out, nil
}
