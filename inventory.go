package gateway

import "context"

// BatteryStatus holds the live operational state of a single Encharge battery,
// as reported by GET /ivp/ensemble/inventory.
//
// This is a richer complement to the Device records from Devices(): it adds
// real-time telemetry (state of charge, temperatures, RF signal strength,
// firmware version) that device_list does not expose.
type BatteryStatus struct {
	SerialNum       string   `json:"serial_num"`
	PercentFull     int      `json:"percentFull"`        // State of charge, 0–100
	Temperature     int      `json:"temperature"`         // °C
	MaxCellTemp     int      `json:"maxCellTemp"`         // °C; highest individual cell temperature
	CapacityWh      int      `json:"encharge_capacity"`   // Nameplate capacity in Wh
	Phase           string   `json:"phase"`               // "ph-a", "ph-b", "ph-c"
	GridMode        string   `json:"Enchg_grid_mode"`     // e.g. "multimode-ongrid"
	AdminStateStr   string   `json:"admin_state_str"`     // e.g. "adminOn"
	MainsAdminState string   `json:"mains_admin_state"`   // e.g. "closed"
	MainsOperState  string   `json:"mains_oper_state"`    // e.g. "closed"
	Communicating   bool     `json:"communicating"`
	SleepEnabled    bool     `json:"sleep_enabled"`
	DCSwitchOff     bool     `json:"dc_switch_off"`
	CommLevelSubGHz int      `json:"comm_level_sub_ghz"`  // RF signal strength, 0–4
	CommLevel24GHz  int      `json:"comm_level_2_4_ghz"`  // RF signal strength, 0–4
	DeviceStatus    []string `json:"device_status"`
	Firmware        string   `json:"img_pnum_running"`    // Firmware version string
	PartNum         string   `json:"part_num"`
	LastReportDate  int64    `json:"last_rpt_date"`       // Unix timestamp
	InstalledDate   int64    `json:"installed"`            // Unix timestamp
}

// inventoryEntry is the per-device-class envelope in the /ivp/ensemble/inventory array.
// The response is a top-level array; each element covers one device class ("ENCHARGE",
// "ENPOWER", etc.) and lists its devices under the "devices" key.
type inventoryEntry struct {
	Type    string         `json:"type"`
	Devices []BatteryStatus `json:"devices"`
}

// BatteryInventory returns the live operational state of all Encharge batteries
// provisioned on this gateway (GET /ivp/ensemble/inventory).
//
// Each BatteryStatus includes state of charge, temperatures, firmware version,
// RF signal strength, and grid mode — telemetry not available from Devices().
// Returns an empty slice (not an error) when no Encharge units are provisioned.
func (c *Client) BatteryInventory(ctx context.Context) ([]BatteryStatus, error) {
	var entries []inventoryEntry
	if err := c.doJSON(ctx, "/ivp/ensemble/inventory", &entries); err != nil {
		return nil, err
	}
	for _, e := range entries {
		if e.Type == "ENCHARGE" {
			if e.Devices == nil {
				return []BatteryStatus{}, nil
			}
			return e.Devices, nil
		}
	}
	return []BatteryStatus{}, nil
}
