package gateway

import "context"

// MeterSummary contains the aggregate power reading for one energy source.
// Values are in milliwatts; use ActiveWatts() for the Watts equivalent.
//
// Sign conventions vary by source — see LiveMeters field comments.
type MeterSummary struct {
	AggPowerMW     int64 `json:"agg_p_mw"`      // Aggregate active power (mW)
	AggApparentMVA int64 `json:"agg_s_mva"`     // Aggregate apparent power (mVA)
	PhaseAMW       int64 `json:"agg_p_ph_a_mw"` // Phase A active power (mW)
	PhaseBMW       int64 `json:"agg_p_ph_b_mw"` // Phase B active power (mW)
	PhaseCMW       int64 `json:"agg_p_ph_c_mw"` // Phase C active power (mW)
	PhaseAMVA      int64 `json:"agg_s_ph_a_mva"`
	PhaseBMVA      int64 `json:"agg_s_ph_b_mva"`
	PhaseCMVA      int64 `json:"agg_s_ph_c_mva"`
}

// ActiveWatts converts the aggregate milliwatt reading to Watts.
func (m MeterSummary) ActiveWatts() float64 {
	return float64(m.AggPowerMW) / 1000
}

// LiveMeters holds real-time readings for every energy source on the system.
//
// Sign conventions (from Enphase spec):
//   - PV:      always positive (generating)
//   - Storage: negative = charging (consuming power), positive = discharging (supplying power)
//   - Grid:    negative = exporting to grid, positive = importing from grid
//   - Load:    always positive (consuming)
type LiveMeters struct {
	LastUpdate   int64        `json:"last_update"`    // Unix epoch of this reading
	SOC          int          `json:"soc"`            // Overall battery SoC (%)
	EncAggSOC    int          `json:"enc_agg_soc"`    // Encharge aggregate SoC (%)
	EncAggEnergy int          `json:"enc_agg_energy"` // Encharge total stored energy (Wh)
	ACBAggSOC    int          `json:"acb_agg_soc"`    // AC Battery aggregate SoC (%)
	ACBAggEnergy int          `json:"acb_agg_energy"` // AC Battery stored energy (Wh)
	IsSplitPhase int          `json:"is_split_phase"` // 1 if split-phase grid
	PhaseCount   int          `json:"phase_count"`
	PV           MeterSummary `json:"pv"`        // Solar panel production
	Storage      MeterSummary `json:"storage"`   // Battery (neg=charging, pos=discharging)
	Grid         MeterSummary `json:"grid"`      // Grid tie (neg=exporting, pos=importing)
	Load         MeterSummary `json:"load"`      // Total home consumption
	Generator    MeterSummary `json:"generator"` // Generator (if present)
}

// LiveData is the complete response from GET /ivp/livedata/status.
type LiveData struct {
	Connection struct {
		MQTTState string `json:"mqtt_state"`
		ProvState string `json:"prov_state"`
		AuthState string `json:"auth_state"`
		SCStream  string `json:"sc_stream"`
	} `json:"connection"`
	Meters LiveMeters `json:"meters"`
	Tasks  struct {
		TaskID    int64 `json:"task_id"`
		Timestamp int64 `json:"timestamp"`
	} `json:"tasks"`
}

// LiveData returns the most real-time power readings available from the gateway.
// It provides a simultaneous breakdown of solar (pv), battery (storage), grid,
// and load consumption — all in milliwatts with per-phase detail.
//
// This is the recommended endpoint for real-time monitoring dashboards.
// For a convenience wrapper, pass the result to SnapshotFromLiveData.
func (c *Client) LiveData(ctx context.Context) (LiveData, error) {
	var out LiveData
	if err := c.doJSON(ctx, "/ivp/livedata/status", &out); err != nil {
		return LiveData{}, err
	}
	return out, nil
}
