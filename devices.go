package gateway

import "context"

// DeviceType enumerates known device categories reported by the gateway.
type DeviceType int

const (
	DeviceTypeUnknown      DeviceType = 0
	DeviceTypeStorage      DeviceType = 13 // Encharge battery
	DeviceTypeMicroinverter DeviceType = 14
)

// Device is a single provisioned device on the IQ Gateway.
type Device struct {
	SerialNumber    string     `json:"serial_number"`
	DeviceType      DeviceType `json:"device_type"`
	ComInterface    int        `json:"com_interface"`
	ComInterfaceStr string     `json:"com_interface_str"` // e.g. "CAN"
	Status          string     `json:"status"`            // "Connected", "Unknown", etc.
	DevInfo         struct {
		Capacity int `json:"capacity"`  // Wh (batteries only)
		DERIndex int `json:"DER_Index"` // Connected phase: 1=L1, 2=L2, 3=L3
	} `json:"dev_info"`
}

// DeviceList is the response from GET /ivp/ensemble/device_list.
type DeviceList struct {
	USB struct {
		CK2Bridge string `json:"ck2_bridge"`
		AutoScan  string `json:"auto_scan"`
	} `json:"usb"`
	Devices []Device `json:"devices"`
}

// Devices returns the commissioning status of all provisioned devices,
// including Encharge batteries (DeviceType 13) and microinverters (DeviceType 14).
func (c *Client) Devices(ctx context.Context) (DeviceList, error) {
	var out DeviceList
	if err := c.doJSON(ctx, "/ivp/ensemble/device_list", &out); err != nil {
		return DeviceList{}, err
	}
	return out, nil
}
