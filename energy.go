package gateway

import "context"

// EnergyBucket contains energy totals and current power for one metering source.
type EnergyBucket struct {
	WattHoursToday     int `json:"wattHoursToday"`
	WattHoursSevenDays int `json:"wattHoursSevenDays"`
	WattHoursLifetime  int `json:"wattHoursLifetime"`
	WattsNow           int `json:"wattsNow"` // Current active power (W)
}

// EnergyData contains energy totals broken down by source and meter type.
// Unlike Production, this endpoint works even when no production CT is installed.
type EnergyData struct {
	Production struct {
		PCU EnergyBucket `json:"pcu"` // Microinverter aggregate (Power Conversion Units)
		RGM EnergyBucket `json:"rgm"` // Revenue Grade Meter (if installed)
		EIM EnergyBucket `json:"eim"` // Envoy Internal production Meter
	} `json:"production"`
	Consumption struct {
		EIM EnergyBucket `json:"eim"` // Envoy Internal consumption Meter
	} `json:"consumption"`
}

// Energy returns energy totals from all available meter types.
// PCU (microinverter sum) is always populated; EIM fields require a metered gateway.
func (c *Client) Energy(ctx context.Context) (EnergyData, error) {
	var out EnergyData
	if err := c.doJSON(ctx, "/ivp/pdm/energy", &out); err != nil {
		return EnergyData{}, err
	}
	return out, nil
}
