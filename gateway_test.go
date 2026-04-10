package gateway_test

import (
	"context"
	"encoding/json"
	"encoding/xml"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/hobeone/enphase-gateway"
)

// newTestClient creates a gateway Client pointed at a test server.
// The test server's HTTP client is used so TLS is bypassed entirely.
func newTestClient(srv *httptest.Server, jwt string) *gateway.Client {
	return gateway.NewClient(srv.URL, jwt, gateway.WithHTTPClient(srv.Client()))
}

// serve registers a handler for path on a new httptest.Server and returns
// the server and a cancellation function.
func serve(t *testing.T, path string, handler http.HandlerFunc) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc(path, handler)
	return httptest.NewServer(mux)
}

// serveJSON is a convenience handler that writes statusCode and marshals body as JSON.
func serveJSON(t *testing.T, statusCode int, body any) http.HandlerFunc {
	t.Helper()
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(statusCode)
		if body != nil {
			if err := json.NewEncoder(w).Encode(body); err != nil {
				t.Errorf("encode response: %v", err)
			}
		}
	}
}

// assertBearerToken checks that the request carries the expected JWT.
func assertBearerToken(t *testing.T, r *http.Request, want string) {
	t.Helper()
	got := r.Header.Get("Authorization")
	if got != "Bearer "+want {
		t.Errorf("Authorization header = %q, want %q", got, "Bearer "+want)
	}
}

// ---------- LiveData ----------

func TestClient_LiveData(t *testing.T) {
	const jwt = "test-jwt"

	payload := map[string]any{
		"connection": map[string]any{
			"mqtt_state": "connected",
			"auth_state": "ok",
		},
		"meters": map[string]any{
			"last_update":   int64(1700000000),
			"soc":           85,
			"enc_agg_soc":   85,
			"enc_agg_energy": 8000,
			"pv":            map[string]any{"agg_p_mw": int64(3500000)},   // 3500 W
			"storage":       map[string]any{"agg_p_mw": int64(-800000)},   // -800 W (charging)
			"grid":          map[string]any{"agg_p_mw": int64(0)},
			"load":          map[string]any{"agg_p_mw": int64(2700000)},   // 2700 W
			"generator":     map[string]any{"agg_p_mw": int64(0)},
		},
	}

	srv := serve(t, "/ivp/livedata/status", func(w http.ResponseWriter, r *http.Request) {
		assertBearerToken(t, r, jwt)
		serveJSON(t, http.StatusOK, payload)(w, r)
	})
	defer srv.Close()

	client := newTestClient(srv, jwt)
	ld, err := client.LiveData(context.Background())
	if err != nil {
		t.Fatalf("LiveData: %v", err)
	}

	if ld.Meters.EncAggSOC != 85 {
		t.Errorf("EncAggSOC = %d, want 85", ld.Meters.EncAggSOC)
	}
	if ld.Meters.PV.AggPowerMW != 3500000 {
		t.Errorf("PV.AggPowerMW = %d, want 3500000", ld.Meters.PV.AggPowerMW)
	}
	if ld.Meters.Storage.AggPowerMW != -800000 {
		t.Errorf("Storage.AggPowerMW = %d, want -800000", ld.Meters.Storage.AggPowerMW)
	}
	if ld.Meters.Load.AggPowerMW != 2700000 {
		t.Errorf("Load.AggPowerMW = %d, want 2700000", ld.Meters.Load.AggPowerMW)
	}
	if got := ld.Meters.PV.ActiveWatts(); got != 3500 {
		t.Errorf("PV.ActiveWatts() = %.1f, want 3500", got)
	}
}

func TestClient_LiveData_Unauthorized(t *testing.T) {
	srv := serve(t, "/ivp/livedata/status", serveJSON(t, http.StatusUnauthorized, nil))
	defer srv.Close()

	_, err := newTestClient(srv, "expired").LiveData(context.Background())
	if !gateway.IsUnauthorized(err) {
		t.Errorf("expected IsUnauthorized, got %v", err)
	}
}

// ---------- GridReadings ----------

func TestClient_GridReadings(t *testing.T) {
	payload := []map[string]any{
		{
			"channels": []map[string]any{
				{"phase": "L1", "activePower": -658.6, "reactivePower": -60.2, "voltage": 229.6, "current": 2.9, "freq": 60.0},
				{"phase": "L2", "activePower": -676.4, "reactivePower": -67.4, "voltage": 230.2, "current": 2.9, "freq": 60.0},
			},
		},
	}

	srv := serve(t, "/ivp/meters/gridReading", serveJSON(t, http.StatusOK, payload))
	defer srv.Close()

	readings, err := newTestClient(srv, "tok").GridReadings(context.Background())
	if err != nil {
		t.Fatalf("GridReadings: %v", err)
	}
	if len(readings) != 1 {
		t.Fatalf("len(readings) = %d, want 1", len(readings))
	}
	if len(readings[0].Channels) != 2 {
		t.Fatalf("len(channels) = %d, want 2", len(readings[0].Channels))
	}
	ch := readings[0].Channels[0]
	if ch.Phase != "L1" {
		t.Errorf("Phase = %q, want L1", ch.Phase)
	}
	if ch.ActivePower >= 0 {
		t.Errorf("L1 ActivePower = %.1f; expected negative (exporting)", ch.ActivePower)
	}
}

// ---------- MeterReadings ----------

func TestClient_MeterReadings(t *testing.T) {
	payload := []map[string]any{
		{
			"eid":         704643328,
			"timestamp":   1654218661,
			"activePower": 132.118,
			"voltage":     246.377,
			"current":     43.257,
			"freq":        59.188,
			"channels":    []map[string]any{},
		},
	}

	srv := serve(t, "/ivp/meters/readings", serveJSON(t, http.StatusOK, payload))
	defer srv.Close()

	readings, err := newTestClient(srv, "tok").MeterReadings(context.Background())
	if err != nil {
		t.Fatalf("MeterReadings: %v", err)
	}
	if len(readings) != 1 {
		t.Fatalf("len = %d, want 1", len(readings))
	}
	if readings[0].ActivePower != 132.118 {
		t.Errorf("ActivePower = %v, want 132.118", readings[0].ActivePower)
	}
}

func TestClient_MeterReadings_NotFound(t *testing.T) {
	srv := serve(t, "/ivp/meters/readings", serveJSON(t, http.StatusNotFound, nil))
	defer srv.Close()

	_, err := newTestClient(srv, "tok").MeterReadings(context.Background())
	if !gateway.IsNotFound(err) {
		t.Errorf("expected IsNotFound, got %v", err)
	}
}

// ---------- Production ----------

func TestClient_Production(t *testing.T) {
	payload := map[string]any{
		"wattHoursToday":     21674,
		"wattHoursSevenDays": 719543,
		"wattHoursLifetime":  1608587,
		"wattsNow":           227,
	}

	srv := serve(t, "/api/v1/production", serveJSON(t, http.StatusOK, payload))
	defer srv.Close()

	p, err := newTestClient(srv, "tok").Production(context.Background())
	if err != nil {
		t.Fatalf("Production: %v", err)
	}
	if p.WattsNow != 227 {
		t.Errorf("WattsNow = %d, want 227", p.WattsNow)
	}
	if p.WattHoursToday != 21674 {
		t.Errorf("WattHoursToday = %d, want 21674", p.WattHoursToday)
	}
}

// ---------- Inverters ----------

func TestClient_Inverters(t *testing.T) {
	payload := []map[string]any{
		{"serialNumber": "121935144671", "lastReportDate": int64(1654171836), "devType": 1, "lastReportWatts": 285, "maxReportWatts": 300},
		{"serialNumber": "121935144672", "lastReportDate": int64(1654171836), "devType": 1, "lastReportWatts": 290, "maxReportWatts": 300},
	}

	srv := serve(t, "/api/v1/production/inverters", serveJSON(t, http.StatusOK, payload))
	defer srv.Close()

	invs, err := newTestClient(srv, "tok").Inverters(context.Background())
	if err != nil {
		t.Fatalf("Inverters: %v", err)
	}
	if len(invs) != 2 {
		t.Fatalf("len = %d, want 2", len(invs))
	}
	if invs[0].SerialNumber != "121935144671" {
		t.Errorf("SerialNumber = %q", invs[0].SerialNumber)
	}
	if invs[0].LastReportWatts != 285 {
		t.Errorf("LastReportWatts = %d, want 285", invs[0].LastReportWatts)
	}
}

// ---------- Meters (config) ----------

func TestClient_Meters(t *testing.T) {
	payload := []map[string]any{
		{"eid": 704643328, "state": "enabled", "measurementType": "production", "phaseMode": "split", "phaseCount": 2, "meteringStatus": "normal"},
		{"eid": 704643584, "state": "enabled", "measurementType": "net-consumption", "phaseMode": "split", "phaseCount": 2, "meteringStatus": "normal"},
	}

	srv := serve(t, "/ivp/meters", serveJSON(t, http.StatusOK, payload))
	defer srv.Close()

	meters, err := newTestClient(srv, "tok").Meters(context.Background())
	if err != nil {
		t.Fatalf("Meters: %v", err)
	}
	if len(meters) != 2 {
		t.Fatalf("len = %d, want 2", len(meters))
	}
	if meters[0].MeasurementType != "production" {
		t.Errorf("MeasurementType = %q, want production", meters[0].MeasurementType)
	}
}

// ---------- Consumption ----------

func TestClient_Consumption(t *testing.T) {
	payload := map[string]any{
		"createdAt":  int64(1654625079),
		"reportType": "net-consumption",
		"cumulative": map[string]any{"currW": 119.423, "rmsVoltage": 241.427, "freqHz": 60.0},
		"lines":      []map[string]any{{"currW": 56.672}, {"currW": 62.751}},
	}

	srv := serve(t, "/ivp/meters/reports/consumption", serveJSON(t, http.StatusOK, payload))
	defer srv.Close()

	r, err := newTestClient(srv, "tok").Consumption(context.Background())
	if err != nil {
		t.Fatalf("Consumption: %v", err)
	}
	if r.ReportType != "net-consumption" {
		t.Errorf("ReportType = %q, want net-consumption", r.ReportType)
	}
	if r.Cumulative.CurrW != 119.423 {
		t.Errorf("Cumulative.CurrW = %v, want 119.423", r.Cumulative.CurrW)
	}
	if len(r.Lines) != 2 {
		t.Errorf("len(Lines) = %d, want 2", len(r.Lines))
	}
}

// ---------- Devices ----------

func TestClient_Devices(t *testing.T) {
	payload := map[string]any{
		"usb": map[string]any{"ck2_bridge": "connected", "auto_scan": "false"},
		"devices": []map[string]any{
			{"serial_number": "492203008650", "device_type": 13, "status": "Connected",
				"dev_info": map[string]any{"capacity": 4960, "DER_Index": 1}},
		},
	}

	srv := serve(t, "/ivp/ensemble/device_list", serveJSON(t, http.StatusOK, payload))
	defer srv.Close()

	dl, err := newTestClient(srv, "tok").Devices(context.Background())
	if err != nil {
		t.Fatalf("Devices: %v", err)
	}
	if len(dl.Devices) != 1 {
		t.Fatalf("len = %d, want 1", len(dl.Devices))
	}
	d := dl.Devices[0]
	if d.DeviceType != gateway.DeviceTypeStorage {
		t.Errorf("DeviceType = %d, want %d (storage)", d.DeviceType, gateway.DeviceTypeStorage)
	}
	if d.DevInfo.Capacity != 4960 {
		t.Errorf("Capacity = %d, want 4960", d.DevInfo.Capacity)
	}
}

// ---------- SystemInfo (XML) ----------

func TestClient_SystemInfo(t *testing.T) {
	xmlBody, _ := xml.Marshal(struct {
		XMLName  xml.Name `xml:"envoy_info"`
		Time     int64    `xml:"time"`
		Device   struct {
			SN       string `xml:"sn"`
			Software string `xml:"software"`
		} `xml:"device"`
	}{
		Time: 1658403712,
		Device: struct {
			SN       string `xml:"sn"`
			Software string `xml:"software"`
		}{SN: "122125067699", Software: "D7.4.22"},
	})

	srv := serve(t, "/info", func(w http.ResponseWriter, r *http.Request) {
		// /info does not require a JWT; verify no auth header is mandatory.
		w.Header().Set("Content-Type", "application/xml")
		w.WriteHeader(http.StatusOK)
		if _, err := w.Write(xmlBody); err != nil {
			t.Errorf("write XML body: %v", err)
		}
	})
	defer srv.Close()

	info, err := newTestClient(srv, "").SystemInfo(context.Background())
	if err != nil {
		t.Fatalf("SystemInfo: %v", err)
	}
	if info.Device.SerialNumber != "122125067699" {
		t.Errorf("SerialNumber = %q, want 122125067699", info.Device.SerialNumber)
	}
	if info.Device.Software != "D7.4.22" {
		t.Errorf("Software = %q, want D7.4.22", info.Device.Software)
	}
}

// ---------- Energy ----------

func TestClient_Energy(t *testing.T) {
	payload := map[string]any{
		"production": map[string]any{
			"pcu": map[string]any{"wattHoursToday": 13251, "wattsNow": 596},
			"rgm": map[string]any{"wattHoursToday": 0, "wattsNow": 0},
			"eim": map[string]any{"wattHoursToday": 0, "wattsNow": 0},
		},
		"consumption": map[string]any{
			"eim": map[string]any{"wattHoursToday": 0, "wattsNow": 0},
		},
	}

	srv := serve(t, "/ivp/pdm/energy", serveJSON(t, http.StatusOK, payload))
	defer srv.Close()

	e, err := newTestClient(srv, "tok").Energy(context.Background())
	if err != nil {
		t.Fatalf("Energy: %v", err)
	}
	if e.Production.PCU.WattsNow != 596 {
		t.Errorf("PCU.WattsNow = %d, want 596", e.Production.PCU.WattsNow)
	}
	if e.Production.PCU.WattHoursToday != 13251 {
		t.Errorf("PCU.WattHoursToday = %d, want 13251", e.Production.PCU.WattHoursToday)
	}
}

// ---------- SnapshotFromLiveData ----------

func TestSnapshotFromLiveData_Exporting(t *testing.T) {
	// Scenario: solar 4000W, battery charging 500W, home load 1500W,
	// surplus 2000W being exported to grid.
	ld := gateway.LiveData{}
	ld.Meters.LastUpdate = 1700000000
	ld.Meters.PV.AggPowerMW = 4000_000      // 4000 W solar
	ld.Meters.Storage.AggPowerMW = -500_000  // -500 W = charging
	ld.Meters.Grid.AggPowerMW = -2000_000    // -2000 W = exporting
	ld.Meters.Load.AggPowerMW = 1500_000     // 1500 W load
	ld.Meters.EncAggSOC = 45
	ld.Meters.EncAggEnergy = 5000

	s := gateway.SnapshotFromLiveData(ld)

	assertEqual(t, "SolarW", 4000.0, s.SolarW)
	assertEqual(t, "BatteryW", -500.0, s.BatteryW)
	assertEqual(t, "GridW", -2000.0, s.GridW)
	assertEqual(t, "LoadW", 1500.0, s.LoadW)
	assertEqual(t, "BatterySOC", 45, s.BatterySOC)
	assertEqual(t, "BatteryWh", 5000, s.BatteryWh)

	// Derived flows.
	assertEqual(t, "SolarToGrid", 2000.0, s.SolarToGrid)
	assertEqual(t, "GridToLoad", 0.0, s.GridToLoad)
	assertEqual(t, "BattToLoad", 0.0, s.BattToLoad)
	assertEqual(t, "SolarToBatt", 500.0, s.SolarToBatt)
	assertEqual(t, "SolarToLoad", 1500.0, s.SolarToLoad)

	if !s.IsExporting() {
		t.Error("expected IsExporting() = true")
	}
	if s.IsImporting() {
		t.Error("expected IsImporting() = false")
	}
	if !s.IsCharging() {
		t.Error("expected IsCharging() = true")
	}
}

func TestSnapshotFromLiveData_Importing(t *testing.T) {
	// Scenario: no solar, battery discharging 1000W, home drawing 3000W,
	// so 2000W coming from grid.
	ld := gateway.LiveData{}
	ld.Meters.LastUpdate = 1700000000
	ld.Meters.PV.AggPowerMW = 0
	ld.Meters.Storage.AggPowerMW = 1000_000 // 1000 W discharging
	ld.Meters.Grid.AggPowerMW = 2000_000    // 2000 W importing
	ld.Meters.Load.AggPowerMW = 3000_000    // 3000 W load
	ld.Meters.EncAggSOC = 20

	s := gateway.SnapshotFromLiveData(ld)

	assertEqual(t, "SolarW", 0.0, s.SolarW)
	assertEqual(t, "BatteryW", 1000.0, s.BatteryW)
	assertEqual(t, "GridW", 2000.0, s.GridW)
	assertEqual(t, "LoadW", 3000.0, s.LoadW)

	assertEqual(t, "SolarToGrid", 0.0, s.SolarToGrid)
	assertEqual(t, "GridToLoad", 2000.0, s.GridToLoad)
	assertEqual(t, "BattToLoad", 1000.0, s.BattToLoad)
	assertEqual(t, "SolarToLoad", 0.0, s.SolarToLoad)

	if !s.IsImporting() {
		t.Error("expected IsImporting() = true")
	}
	if !s.IsDischarging() {
		t.Error("expected IsDischarging() = true")
	}
}

func TestSnapshotFromLiveData_SelfSufficiency(t *testing.T) {
	cases := []struct {
		name    string
		loadW   float64
		gridW   float64
		want    float64
	}{
		{"fully_off_grid", 2000, 0, 1.0},
		{"half_grid", 2000, 1000, 0.5},
		{"fully_grid", 2000, 2000, 0.0},
		{"exporting", 1000, -500, 1.0},
		{"no_load", 0, 0, 1.0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ld := gateway.LiveData{}
			ld.Meters.Load.AggPowerMW = int64(tc.loadW * 1000)
			ld.Meters.Grid.AggPowerMW = int64(tc.gridW * 1000)
			s := gateway.SnapshotFromLiveData(ld)
			if got := s.SelfSufficiency(); got != tc.want {
				t.Errorf("SelfSufficiency() = %.2f, want %.2f", got, tc.want)
			}
		})
	}
}

// ---------- ParseExpiry ----------

func TestParseExpiry(t *testing.T) {
	// Build a minimal JWT with a known exp claim.
	// Header: {"alg":"HS256","typ":"JWT"}
	// Payload: {"exp":1690568380}
	// (Signature is a dummy; we never verify it.)
	const rawJWT = "eyJhbGciOiJIUzI1NiIsInR5cCI6IkpXVCJ9" +
		".eyJleHAiOjE2OTA1NjgzODB9" +
		".dummy_signature"

	expiry, err := gateway.ParseExpiry(rawJWT)
	if err != nil {
		t.Fatalf("ParseExpiry: %v", err)
	}

	want := time.Unix(1690568380, 0)
	if !expiry.Equal(want) {
		t.Errorf("expiry = %v, want %v", expiry, want)
	}
}

func TestParseExpiry_InvalidJWT(t *testing.T) {
	cases := []struct{ name, jwt string }{
		{"empty", ""},
		{"one_part", "notajwt"},
		{"two_parts", "a.b"},
		{"bad_base64", "a.!!!.c"},
		{"no_exp", "eyJhbGciOiJub25lIn0.eyJzdWIiOiJ0ZXN0In0."},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := gateway.ParseExpiry(tc.jwt); err == nil {
				t.Errorf("expected error for JWT %q, got nil", tc.jwt)
			}
		})
	}
}

// ---------- Error helpers ----------

func TestIsUnauthorized(t *testing.T) {
	srv := serve(t, "/ivp/livedata/status", serveJSON(t, http.StatusUnauthorized, nil))
	defer srv.Close()

	_, err := newTestClient(srv, "tok").LiveData(context.Background())
	if !gateway.IsUnauthorized(err) {
		t.Errorf("IsUnauthorized(%v) = false, want true", err)
	}
	if gateway.IsNotFound(err) {
		t.Errorf("IsNotFound(%v) = true, want false", err)
	}
}

func TestIsNotFound(t *testing.T) {
	srv := serve(t, "/ivp/meters/readings", serveJSON(t, http.StatusNotFound, nil))
	defer srv.Close()

	_, err := newTestClient(srv, "tok").MeterReadings(context.Background())
	if !gateway.IsNotFound(err) {
		t.Errorf("IsNotFound(%v) = false, want true", err)
	}
}

// ---------- MeterSummary.ActiveWatts ----------

func TestMeterSummary_ActiveWatts(t *testing.T) {
	cases := []struct {
		mw   int64
		want float64
	}{
		{0, 0},
		{1000, 1},
		{3500000, 3500},
		{-800000, -800},
	}
	for _, tc := range cases {
		m := gateway.MeterSummary{AggPowerMW: tc.mw}
		if got := m.ActiveWatts(); got != tc.want {
			t.Errorf("ActiveWatts(%d mW) = %.3f, want %.3f", tc.mw, got, tc.want)
		}
	}
}

// ---------- Context cancellation ----------

func TestClient_LiveData_ContextCancelled(t *testing.T) {
	srv := serve(t, "/ivp/livedata/status", func(w http.ResponseWriter, r *http.Request) {
		// Block until the client gives up.
		<-r.Context().Done()
	})
	defer srv.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()

	_, err := newTestClient(srv, "tok").LiveData(ctx)
	if err == nil {
		t.Error("expected error on context cancellation, got nil")
	}
}

// ---------- helpers ----------

func assertEqual[T comparable](t *testing.T, name string, want, got T) {
	t.Helper()
	if got != want {
		t.Errorf("%s = %v, want %v", name, got, want)
	}
}
