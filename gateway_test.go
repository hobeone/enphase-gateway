package gateway_test

import (
	"context"
	"encoding/json"
	"encoding/xml"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/hobeone/enphase-gateway"
)

// Compile-time assertion: *Client must satisfy the Gateway interface.
// If Client stops implementing any method, this line fails to compile.
var _ gateway.Gateway = (*gateway.Client)(nil)

// ─────────────────────────── test helpers ────────────────────────────────────

// newTestClient creates a gateway Client pointed at a test server.
// The test server's HTTP client is used so TLS is bypassed entirely.
func newTestClient(srv *httptest.Server, jwt string) *gateway.Client {
	return gateway.NewClient(srv.URL, jwt, gateway.WithHTTPClient(srv.Client()))
}

// serve registers a handler for path on a new httptest.Server and returns the server.
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

// ─────────────────────────── FetchJWT ────────────────────────────────────────

func TestFetchJWT(t *testing.T) {
	t.Parallel()

	const (
		wantUser   = "user@example.com"
		wantSerial = "serial123"
		// Minimal valid JWT: header.{"exp":1690568380}.dummy-signature
		fakeJWT = "eyJhbGciOiJIUzI1NiIsInR5cCI6IkpXVCJ9" +
			".eyJleHAiOjE2OTA1NjgzODB9" +
			".dummy_signature"
	)

	// Step 1 server: Enphase login → returns session_id.
	loginSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/login/login.json" {
			t.Errorf("login: unexpected path %q", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		if err := json.NewEncoder(w).Encode(map[string]string{"session_id": "sess-abc"}); err != nil {
			t.Errorf("encode session_id: %v", err)
		}
	}))
	defer loginSrv.Close()

	// Step 2 server: Entrez token exchange → returns JWT.
	tokenSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/tokens" {
			t.Errorf("token: unexpected path %q", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		if err := json.NewEncoder(w).Encode(map[string]string{"token": fakeJWT}); err != nil {
			t.Errorf("encode token: %v", err)
		}
	}))
	defer tokenSrv.Close()

	resp, err := gateway.FetchJWT(context.Background(), wantUser, "password", wantSerial,
		gateway.WithEnlightenURL(loginSrv.URL),
		gateway.WithEntrezURL(tokenSrv.URL),
	)
	if err != nil {
		t.Fatalf("FetchJWT: %v", err)
	}
	if resp.Token != fakeJWT {
		t.Errorf("Token = %q, want %q", resp.Token, fakeJWT)
	}
	// Expiry should be extracted from the JWT's exp claim even when the server
	// returns only a raw token string (not a JSON object with expires_at).
	if resp.ExpiresAt == 0 {
		t.Error("ExpiresAt should be populated from JWT exp claim")
	}
	if resp.Expiry().IsZero() {
		t.Error("Expiry() should return a non-zero time")
	}
}

// ─────────────────────────── LiveData ────────────────────────────────────────

func TestClient_LiveData(t *testing.T) {
	t.Parallel()
	const jwt = "test-jwt"

	payload := map[string]any{
		"connection": map[string]any{
			"mqtt_state": "connected",
			"auth_state": "ok",
		},
		"meters": map[string]any{
			"last_update":    int64(1700000000),
			"soc":            85,
			"enc_agg_soc":    85,
			"enc_agg_energy": 8000,
			"pv":             map[string]any{"agg_p_mw": int64(3500000)},  // 3500 W
			"storage":        map[string]any{"agg_p_mw": int64(-800000)},  // -800 W (charging)
			"grid":           map[string]any{"agg_p_mw": int64(0)},
			"load":           map[string]any{"agg_p_mw": int64(2700000)},  // 2700 W
			"generator":      map[string]any{"agg_p_mw": int64(0)},
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
	if ld.Meters.EncAggEnergy != 8000 {
		t.Errorf("EncAggEnergy = %d, want 8000", ld.Meters.EncAggEnergy)
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
	t.Parallel()
	srv := serve(t, "/ivp/livedata/status", serveJSON(t, http.StatusUnauthorized, nil))
	defer srv.Close()

	_, err := newTestClient(srv, "expired").LiveData(context.Background())
	if !gateway.IsUnauthorized(err) {
		t.Errorf("expected IsUnauthorized, got %v", err)
	}
}

// ─────────────────────────── GridReadings ────────────────────────────────────

func TestClient_GridReadings(t *testing.T) {
	t.Parallel()
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

// ─────────────────────────── MeterReadings ───────────────────────────────────

func TestClient_MeterReadings(t *testing.T) {
	t.Parallel()
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
	t.Parallel()
	srv := serve(t, "/ivp/meters/readings", serveJSON(t, http.StatusNotFound, nil))
	defer srv.Close()

	_, err := newTestClient(srv, "tok").MeterReadings(context.Background())
	if !gateway.IsNotFound(err) {
		t.Errorf("expected IsNotFound, got %v", err)
	}
}

// ─────────────────────────── Production ──────────────────────────────────────

func TestClient_Production(t *testing.T) {
	t.Parallel()
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

// ─────────────────────────── Inverters ───────────────────────────────────────

func TestClient_Inverters(t *testing.T) {
	t.Parallel()
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

// ─────────────────────────── Meters (config) ─────────────────────────────────

func TestClient_Meters(t *testing.T) {
	t.Parallel()
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

// ─────────────────────────── Consumption ─────────────────────────────────────

func TestClient_Consumption(t *testing.T) {
	t.Parallel()
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

func TestClient_Consumption_NotFound(t *testing.T) {
	t.Parallel()
	// Consumption requires a CT to be installed; gateways without one return 404.
	srv := serve(t, "/ivp/meters/reports/consumption", serveJSON(t, http.StatusNotFound, nil))
	defer srv.Close()

	_, err := newTestClient(srv, "tok").Consumption(context.Background())
	if !gateway.IsNotFound(err) {
		t.Errorf("expected IsNotFound, got %v", err)
	}
}

// ─────────────────────────── Devices ─────────────────────────────────────────

func TestClient_Devices(t *testing.T) {
	t.Parallel()
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

// ─────────────────────────── SystemInfo (XML) ────────────────────────────────

func TestClient_SystemInfo(t *testing.T) {
	t.Parallel()
	xmlBody, _ := xml.Marshal(struct {
		XMLName xml.Name `xml:"envoy_info"`
		Time    int64    `xml:"time"`
		Device  struct {
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
		// /info is the one unauthenticated endpoint — it must NOT forward the JWT.
		if got := r.Header.Get("Authorization"); got != "" {
			t.Errorf("/info should not send Authorization header, got %q", got)
		}
		w.Header().Set("Content-Type", "application/xml")
		w.WriteHeader(http.StatusOK)
		if _, err := w.Write(xmlBody); err != nil {
			t.Errorf("write XML body: %v", err)
		}
	})
	defer srv.Close()

	// Pass a non-empty JWT to confirm SystemInfo doesn't forward it.
	info, err := newTestClient(srv, "some-jwt").SystemInfo(context.Background())
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

// ─────────────────────────── Energy ──────────────────────────────────────────

func TestClient_Energy(t *testing.T) {
	t.Parallel()
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

// ─────────────────────────── SetJWT ──────────────────────────────────────────

func TestClient_SetJWT_Concurrent(t *testing.T) {
	t.Parallel()

	// A minimal server that always succeeds so we can fire requests freely.
	srv := serve(t, "/ivp/livedata/status", serveJSON(t, http.StatusOK, map[string]any{
		"connection": map[string]any{},
		"meters":     map[string]any{},
	}))
	defer srv.Close()

	client := newTestClient(srv, "initial-jwt")

	var wg sync.WaitGroup
	for i := range 10 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			client.SetJWT(fmt.Sprintf("jwt-%d", i))
			_, _ = client.LiveData(context.Background())
		}()
	}
	wg.Wait()
	// No explicit assertion — the race detector (-race) catches any concurrent
	// access violation in SetJWT's mutex or the jwt field in doJSON.
}

// ─────────────────────────── SnapshotFromLiveData ────────────────────────────

func TestSnapshotFromLiveData(t *testing.T) {
	t.Parallel()

	type meters struct {
		pvMW, storageMW, gridMW, loadMW int64
		soc, energyWh                   int
	}
	type want struct {
		solarW, batteryW, gridW, loadW float64
		battSOC, battWh                int
		solarToGrid, gridToLoad        float64
		battToLoad, solarToBatt        float64
		solarToLoad                    float64
		exporting, importing           bool
		charging, discharging          bool
	}
	cases := []struct {
		name string
		in   meters
		want want
	}{
		{
			name: "exporting_solar_surplus",
			// Solar 4000W, battery charging 500W, load 1500W → 2000W exported.
			in: meters{pvMW: 4000_000, storageMW: -500_000, gridMW: -2000_000, loadMW: 1500_000, soc: 45, energyWh: 5000},
			want: want{
				solarW: 4000, batteryW: -500, gridW: -2000, loadW: 1500,
				battSOC: 45, battWh: 5000,
				solarToGrid: 2000, solarToBatt: 500, solarToLoad: 1500,
				exporting: true, charging: true,
			},
		},
		{
			name: "importing_battery_discharging",
			// No solar, battery discharging 1000W, load 3000W → 2000W from grid.
			in: meters{storageMW: 1000_000, gridMW: 2000_000, loadMW: 3000_000, soc: 20},
			want: want{
				batteryW: 1000, gridW: 2000, loadW: 3000,
				battSOC: 20,
				gridToLoad: 2000, battToLoad: 1000,
				importing: true, discharging: true,
			},
		},
		{
			name: "solar_to_load_clamps_at_zero_on_measurement_noise",
			// SolarToGrid + SolarToBatt exceed solar production (as can happen with
			// sensor noise) — SolarToLoad must clamp to 0, not go negative.
			in: meters{pvMW: 3000_000, storageMW: -2000_000, gridMW: -2000_000, loadMW: 0},
			want: want{
				solarW: 3000, batteryW: -2000, gridW: -2000, loadW: 0,
				solarToGrid: 2000, solarToBatt: 2000,
				solarToLoad: 0,
				exporting: true, charging: true,
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			ld := gateway.LiveData{}
			ld.Meters.LastUpdate = 1700000000
			ld.Meters.PV.AggPowerMW = tc.in.pvMW
			ld.Meters.Storage.AggPowerMW = tc.in.storageMW
			ld.Meters.Grid.AggPowerMW = tc.in.gridMW
			ld.Meters.Load.AggPowerMW = tc.in.loadMW
			ld.Meters.EncAggSOC = tc.in.soc
			ld.Meters.EncAggEnergy = tc.in.energyWh

			s := gateway.SnapshotFromLiveData(ld)

			assertEqual(t, "SolarW", tc.want.solarW, s.SolarW)
			assertEqual(t, "BatteryW", tc.want.batteryW, s.BatteryW)
			assertEqual(t, "GridW", tc.want.gridW, s.GridW)
			assertEqual(t, "LoadW", tc.want.loadW, s.LoadW)
			assertEqual(t, "BatterySOC", tc.want.battSOC, s.BatterySOC)
			assertEqual(t, "BatteryWh", tc.want.battWh, s.BatteryWh)
			assertEqual(t, "SolarToGrid", tc.want.solarToGrid, s.SolarToGrid)
			assertEqual(t, "GridToLoad", tc.want.gridToLoad, s.GridToLoad)
			assertEqual(t, "BattToLoad", tc.want.battToLoad, s.BattToLoad)
			assertEqual(t, "SolarToBatt", tc.want.solarToBatt, s.SolarToBatt)
			assertEqual(t, "SolarToLoad", tc.want.solarToLoad, s.SolarToLoad)
			assertEqual(t, "IsExporting", tc.want.exporting, s.IsExporting())
			assertEqual(t, "IsImporting", tc.want.importing, s.IsImporting())
			assertEqual(t, "IsCharging", tc.want.charging, s.IsCharging())
			assertEqual(t, "IsDischarging", tc.want.discharging, s.IsDischarging())
		})
	}
}

func TestSnapshotFromLiveData_SelfSufficiency(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name  string
		loadW float64
		gridW float64
		want  float64
	}{
		{"fully_off_grid", 2000, 0, 1.0},
		{"half_grid", 2000, 1000, 0.5},
		{"fully_grid", 2000, 2000, 0.0},
		{"exporting", 1000, -500, 1.0},
		{"no_load", 0, 0, 1.0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
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

// ─────────────────────────── ParseExpiry ─────────────────────────────────────

func TestParseExpiry(t *testing.T) {
	t.Parallel()
	// Header: {"alg":"HS256","typ":"JWT"}  Payload: {"exp":1690568380}
	// Signature is a dummy — ParseExpiry never verifies it.
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
	t.Parallel()

	cases := []struct{ name, jwt string }{
		{"empty", ""},
		{"one_part", "notajwt"},
		{"two_parts", "a.b"},
		{"bad_base64", "a.!!!.c"},
		{"no_exp", "eyJhbGciOiJub25lIn0.eyJzdWIiOiJ0ZXN0In0."},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if _, err := gateway.ParseExpiry(tc.jwt); err == nil {
				t.Errorf("expected error for JWT %q, got nil", tc.jwt)
			}
		})
	}
}

// ─────────────────────────── Error helpers ───────────────────────────────────

func TestIsUnauthorized(t *testing.T) {
	t.Parallel()
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
	t.Parallel()
	srv := serve(t, "/ivp/meters/readings", serveJSON(t, http.StatusNotFound, nil))
	defer srv.Close()

	_, err := newTestClient(srv, "tok").MeterReadings(context.Background())
	if !gateway.IsNotFound(err) {
		t.Errorf("IsNotFound(%v) = false, want true", err)
	}
}

// ─────────────────────────── MeterSummary.ActiveWatts ────────────────────────

func TestMeterSummary_ActiveWatts(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name string
		mw   int64
		want float64
	}{
		{"zero", 0, 0},
		{"one_watt", 1000, 1},
		{"typical_solar", 3500000, 3500},
		{"negative_charging", -800000, -800},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			m := gateway.MeterSummary{AggPowerMW: tc.mw}
			if got := m.ActiveWatts(); got != tc.want {
				t.Errorf("ActiveWatts(%d mW) = %.3f, want %.3f", tc.mw, got, tc.want)
			}
		})
	}
}

// ─────────────────────────── Context cancellation ────────────────────────────

func TestClient_LiveData_ContextCancelled(t *testing.T) {
	t.Parallel()

	srv := serve(t, "/ivp/livedata/status", func(w http.ResponseWriter, r *http.Request) {
		<-r.Context().Done() // block until the client gives up
	})
	defer srv.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()

	_, err := newTestClient(srv, "tok").LiveData(ctx)
	if err == nil {
		t.Error("expected error on context cancellation, got nil")
	}
}

// ─────────────────────────── BatteryInventory ────────────────────────────────

func TestClient_BatteryInventory(t *testing.T) {
	t.Parallel()
	// The endpoint returns a top-level array; each element covers one device
	// class. Only the "ENCHARGE" element carries battery telemetry.
	payload := []map[string]any{
		{
			"type": "ENCHARGE",
			"devices": []map[string]any{
				{
					"serial_num":          "492203012345",
					"percentFull":         82,
					"temperature":         24,
					"maxCellTemp":         25,
					"encharge_capacity":   4960,
					"phase":               "ph-a",
					"Enchg_grid_mode":     "multimode-ongrid",
					"admin_state_str":     "adminOn",
					"communicating":       true,
					"comm_level_sub_ghz":  4,
					"comm_level_2_4_ghz":  3,
					"img_pnum_running":    "2.6.4813",
					"device_status":       []string{"envoy.global.ok"},
					"last_rpt_date":       1712345678,
				},
			},
		},
		{
			"type":    "ENPOWER",
			"devices": []map[string]any{},
		},
	}

	srv := serve(t, "/ivp/ensemble/inventory", serveJSON(t, http.StatusOK, payload))
	defer srv.Close()

	batteries, err := newTestClient(srv, "tok").BatteryInventory(context.Background())
	if err != nil {
		t.Fatalf("BatteryInventory: %v", err)
	}
	if len(batteries) != 1 {
		t.Fatalf("len = %d, want 1", len(batteries))
	}
	b := batteries[0]
	assertEqual(t, "SerialNum", "492203012345", b.SerialNum)
	assertEqual(t, "PercentFull", 82, b.PercentFull)
	assertEqual(t, "Temperature", 24, b.Temperature)
	assertEqual(t, "CapacityWh", 4960, b.CapacityWh)
	assertEqual(t, "GridMode", "multimode-ongrid", b.GridMode)
	assertEqual(t, "Communicating", true, b.Communicating)
	assertEqual(t, "CommLevelSubGHz", 4, b.CommLevelSubGHz)
	assertEqual(t, "Firmware", "2.6.4813", b.Firmware)
}

func TestClient_BatteryInventory_NoEncharge(t *testing.T) {
	t.Parallel()
	// Gateway with no Encharge units returns only an ENPOWER entry.
	payload := []map[string]any{
		{"type": "ENPOWER", "devices": []map[string]any{}},
	}

	srv := serve(t, "/ivp/ensemble/inventory", serveJSON(t, http.StatusOK, payload))
	defer srv.Close()

	batteries, err := newTestClient(srv, "tok").BatteryInventory(context.Background())
	if err != nil {
		t.Fatalf("BatteryInventory: %v", err)
	}
	if len(batteries) != 0 {
		t.Errorf("len = %d, want 0", len(batteries))
	}
}

// ─────────────────────────── EnableHighFrequencyMode ────────────────────────

// enableModeMux builds an httptest.Server that handles the POST to
// /ivp/livedata/stream, returning the given sc_stream status value.
func enableModeMux(t *testing.T, enableStatus string) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/ivp/livedata/stream", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("/ivp/livedata/stream: want POST, got %s", r.Method)
		}
		w.Header().Set("Content-Type", "application/json")
		if err := json.NewEncoder(w).Encode(map[string]string{"sc_stream": enableStatus}); err != nil {
			t.Errorf("encode sc_stream: %v", err)
		}
	})
	return httptest.NewServer(mux)
}

func TestClient_EnableHighFrequencyMode(t *testing.T) {
	t.Parallel()
	srv := enableModeMux(t, "enabled")
	defer srv.Close()

	if err := newTestClient(srv, "tok").EnableHighFrequencyMode(context.Background()); err != nil {
		t.Fatalf("EnableHighFrequencyMode: %v", err)
	}
}

func TestClient_EnableHighFrequencyMode_AlreadyEnabled(t *testing.T) {
	t.Parallel()
	// Gateway returns "enabled" even when already active — should be a no-op.
	srv := enableModeMux(t, "enabled")
	defer srv.Close()

	client := newTestClient(srv, "tok")
	if err := client.EnableHighFrequencyMode(context.Background()); err != nil {
		t.Fatalf("first call: %v", err)
	}
	if err := client.EnableHighFrequencyMode(context.Background()); err != nil {
		t.Fatalf("second call (already enabled): %v", err)
	}
}

func TestClient_EnableHighFrequencyMode_Fails(t *testing.T) {
	t.Parallel()
	// Gateway returns sc_stream != "enabled" — should be an error.
	mux := http.NewServeMux()
	mux.HandleFunc("/ivp/livedata/stream", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if err := json.NewEncoder(w).Encode(map[string]string{"sc_stream": "disabled"}); err != nil {
			t.Errorf("encode: %v", err)
		}
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	err := newTestClient(srv, "tok").EnableHighFrequencyMode(context.Background())
	if err == nil {
		t.Fatal("expected error when sc_stream=disabled, got nil")
	}
}

// ─────────────────────────── helpers ─────────────────────────────────────────

func assertEqual[T comparable](t *testing.T, name string, want, got T) {
	t.Helper()
	if got != want {
		t.Errorf("%s = %v, want %v", name, got, want)
	}
}
