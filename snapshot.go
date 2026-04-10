package gateway

import "time"

// EnergySnapshot is a human-readable summary of current power flows derived
// from a LiveData response. All power values are in Watts.
//
// Sign conventions — chosen for intuitive readability:
//
//	SolarW:   always >= 0 (solar panels generating)
//	BatteryW: positive = discharging (battery supplying power)
//	          negative = charging (battery consuming power)
//	GridW:    positive = importing from grid (buying power)
//	          negative = exporting to grid (selling/feeding back surplus)
//	LoadW:    always >= 0 (total home consumption)
//
// The power balance identity always holds (within measurement noise):
//
//	LoadW ≈ SolarW + BatteryW + GridW
type EnergySnapshot struct {
	Timestamp  time.Time
	SolarW     float64 // Solar panel output
	BatteryW   float64 // Battery net: positive=discharging, negative=charging
	GridW      float64 // Grid net: positive=importing, negative=exporting
	LoadW      float64 // Total home load
	BatterySOC int     // Battery state of charge (%)
	BatteryWh  int     // Total energy currently stored in battery (Wh)

	// Derived flow breakdown — all values >= 0.
	SolarToLoad  float64 // Solar power going directly to home loads
	SolarToGrid  float64 // Surplus solar exported to grid
	SolarToBatt  float64 // Solar power charging the battery
	GridToLoad   float64 // Load powered by grid import
	BattToLoad   float64 // Load powered by battery discharge
}

// SnapshotFromLiveData computes an EnergySnapshot from a LiveData response.
//
// The LiveData sign conventions for Storage differ from EnergySnapshot:
// in LiveData, Storage.AggPowerMW is negative when charging (the battery
// is consuming power) and positive when discharging (supplying power).
// SnapshotFromLiveData normalises this so BatteryW follows the same
// positive=supplying / negative=consuming convention as GridW.
func SnapshotFromLiveData(d LiveData) EnergySnapshot {
	s := EnergySnapshot{
		Timestamp:  time.Unix(d.Meters.LastUpdate, 0),
		SolarW:     float64(d.Meters.PV.AggPowerMW) / 1000,
		BatteryW:   float64(d.Meters.Storage.AggPowerMW) / 1000,
		GridW:      float64(d.Meters.Grid.AggPowerMW) / 1000,
		LoadW:      float64(d.Meters.Load.AggPowerMW) / 1000,
		BatterySOC: d.Meters.EncAggSOC,
		BatteryWh:  d.Meters.EncAggEnergy,
	}

	// Derived flows.
	if s.GridW < 0 {
		// Exporting: surplus is going to the grid.
		s.SolarToGrid = -s.GridW
		s.GridToLoad = 0
	} else {
		s.GridToLoad = s.GridW
		s.SolarToGrid = 0
	}

	if s.BatteryW > 0 {
		// Discharging: battery is helping power the home.
		s.BattToLoad = s.BatteryW
		s.SolarToBatt = 0
	} else if s.BatteryW < 0 {
		// Charging: solar or grid is filling the battery.
		s.SolarToBatt = -s.BatteryW
		s.BattToLoad = 0
	}

	// Solar directly to load = total solar minus what went to grid or battery.
	s.SolarToLoad = s.SolarW - s.SolarToGrid - s.SolarToBatt
	if s.SolarToLoad < 0 {
		s.SolarToLoad = 0
	}

	return s
}

// IsExporting reports whether the system is currently selling power to the grid.
func (s EnergySnapshot) IsExporting() bool { return s.GridW < -0.5 }

// IsImporting reports whether the system is currently buying power from the grid.
func (s EnergySnapshot) IsImporting() bool { return s.GridW > 0.5 }

// IsCharging reports whether the battery is currently being charged.
func (s EnergySnapshot) IsCharging() bool { return s.BatteryW < -0.5 }

// IsDischarging reports whether the battery is currently supplying power.
func (s EnergySnapshot) IsDischarging() bool { return s.BatteryW > 0.5 }

// SelfSufficiency returns the fraction of load power currently met without
// drawing from the grid (0.0–1.0). A value of 1.0 means fully off-grid.
func (s EnergySnapshot) SelfSufficiency() float64 {
	if s.LoadW <= 0 {
		return 1.0
	}
	gridFraction := s.GridW / s.LoadW
	if gridFraction < 0 {
		return 1.0
	}
	if gridFraction > 1 {
		return 0.0
	}
	return 1.0 - gridFraction
}
