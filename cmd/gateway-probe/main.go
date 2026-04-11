// gateway-probe exercises every API endpoint on a real Enphase IQ Gateway
// and prints the raw HTTP exchange alongside the parsed Go struct for each one.
// It is an integration-testing tool, not production code.
//
// Usage:
//
//	# With a config file (recommended — keeps credentials out of shell history):
//	gateway-probe -config probe.json
//
//	# With flags:
//	gateway-probe -addr envoy.local -username user@example.com -password s3cr3t -serial 123456789012
//
//	# Skip cloud auth if you already have a JWT:
//	gateway-probe -addr envoy.local -jwt eyJ...
//
// Config file format (probe.json):
//
//	{
//	  "addr":     "envoy.local",
//	  "username": "user@example.com",
//	  "password": "s3cr3t",
//	  "serial":   "123456789012",
//	  "jwt":      ""
//	}
package main

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"

	gateway "github.com/hobeone/enphase-gateway"
)

// ── Config ────────────────────────────────────────────────────────────────────

// config holds all settings for the probe.  Fields are populated first from a
// JSON file, then overridden by any explicit CLI flags.
type config struct {
	Addr     string `json:"addr"`
	Username string `json:"username"`
	Password string `json:"password"`
	Serial   string `json:"serial"`
	JWT      string `json:"jwt"` // pre-fetched token; skips FetchJWT when non-empty
}

func loadConfigFile(path string) (config, error) {
	f, err := os.Open(path)
	if err != nil {
		return config{}, err
	}
	defer f.Close()
	var cfg config
	if err := json.NewDecoder(f).Decode(&cfg); err != nil {
		return config{}, fmt.Errorf("parse %s: %w", path, err)
	}
	return cfg, nil
}

// ── Debug transport ───────────────────────────────────────────────────────────

// debugTransport wraps an inner RoundTripper and writes every request and
// response (including the raw body) to out.  The Authorization header value
// is always redacted.
type debugTransport struct {
	wrapped http.RoundTripper
	out     io.Writer
}

func (d *debugTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	fmt.Fprintf(d.out, "→ %s %s\n", req.Method, req.URL)
	for k, vs := range req.Header {
		if strings.EqualFold(k, "authorization") {
			fmt.Fprintf(d.out, "  %s: [redacted]\n", k)
		} else {
			fmt.Fprintf(d.out, "  %s: %s\n", k, strings.Join(vs, ", "))
		}
	}

	resp, err := d.wrapped.RoundTrip(req)
	if err != nil {
		fmt.Fprintf(d.out, "  network error: %v\n", err)
		return nil, err
	}

	// Read the body fully so we can log it, then replace it for the caller.
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	resp.Body.Close()
	if err != nil {
		return nil, fmt.Errorf("read response body: %w", err)
	}
	resp.Body = io.NopCloser(bytes.NewReader(body))

	fmt.Fprintf(d.out, "← %s  (%d bytes)\n", resp.Status, len(body))
	if len(body) > 0 {
		fmt.Fprintln(d.out, indentJSON(body))
	}
	return resp, nil
}

// indentJSON pretty-prints raw bytes if they are valid JSON, or returns them
// as-is (e.g. XML from /info).
func indentJSON(raw []byte) string {
	var buf bytes.Buffer
	if err := json.Indent(&buf, raw, "  ", "  "); err == nil {
		return "  " + buf.String()
	}
	// Not JSON (e.g. XML) — print verbatim with consistent indentation.
	return "  " + string(raw)
}

// ── Output helpers ────────────────────────────────────────────────────────────

const rule = "════════════════════════════════════════════════════════"

func section(name, method, path string) {
	fmt.Printf("\n%s\n  %-20s  %s %s\n%s\n", rule, name, method, path, rule)
}

// printParsed re-marshals v as indented JSON so we see the Go field names
// alongside the raw wire names logged by the transport.
func printParsed(v any) {
	b, err := json.MarshalIndent(v, "  ", "  ")
	if err != nil {
		fmt.Printf("  [marshal error: %v]\n", err)
		return
	}
	fmt.Printf("  parsed →\n  %s\n", b)
}

// ── Main ──────────────────────────────────────────────────────────────────────

func main() {
	var (
		configFile = flag.String("config", "probe.json", "path to JSON config file")
		addr       = flag.String("addr", "", "gateway hostname or IP (overrides config)")
		username   = flag.String("username", "", "Enphase account email (overrides config)")
		password   = flag.String("password", "", "Enphase account password (overrides config)")
		serial     = flag.String("serial", "", "gateway serial number (overrides config)")
		jwt        = flag.String("jwt", "", "pre-fetched JWT; skips cloud auth when set (overrides config)")
	)
	flag.Parse()

	// Load config file; silently ignore a missing file, fail on parse error.
	cfg, err := loadConfigFile(*configFile)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		fatalf("config: %v", err)
	}

	// CLI flags override file values.
	if *addr != "" {
		cfg.Addr = *addr
	}
	if *username != "" {
		cfg.Username = *username
	}
	if *password != "" {
		cfg.Password = *password
	}
	if *serial != "" {
		cfg.Serial = *serial
	}
	if *jwt != "" {
		cfg.JWT = *jwt
	}

	if cfg.Addr == "" {
		fatalf("-addr (or config.addr) is required")
	}

	ctx := context.Background()

	// Resolve JWT — fetch from cloud if not provided.
	if cfg.JWT == "" {
		if cfg.Username == "" || cfg.Password == "" || cfg.Serial == "" {
			fatalf("provide -jwt or all of -username, -password, -serial")
		}
		fmt.Printf("Fetching JWT from Enphase cloud for serial %s ...\n", cfg.Serial)
		tr, err := gateway.FetchJWT(ctx, cfg.Username, cfg.Password, cfg.Serial)
		if err != nil {
			fatalf("FetchJWT: %v", err)
		}
		cfg.JWT = tr.Token
		fmt.Printf("JWT obtained, expires %s\n\n", tr.Expiry().Format(time.RFC3339))
	}

	// Build an HTTP client with the debug-logging transport.
	// We construct the TLS transport ourselves since the library's TLS config
	// is internal; WithHTTPClient replaces the whole client.
	tlsTransport := &http.Transport{
		TLSClientConfig: &tls.Config{InsecureSkipVerify: true}, //nolint:gosec // gateway self-signed cert
	}
	debugClient := &http.Client{
		Transport: &debugTransport{wrapped: tlsTransport, out: os.Stdout},
		Timeout:   15 * time.Second,
	}
	client := gateway.NewClient(cfg.Addr, cfg.JWT, gateway.WithHTTPClient(debugClient))

	// ── SystemInfo ─────────────────────────────────────────────────────────────
	section("SystemInfo", "GET", "/info")
	sysinfo, err := client.SystemInfo(ctx)
	if err != nil {
		fmt.Printf("  error: %v\n", err)
	} else {
		printParsed(sysinfo)
	}

	// ── LiveData ───────────────────────────────────────────────────────────────
	section("LiveData", "GET", "/ivp/livedata/status")
	ld, err := client.LiveData(ctx)
	if err != nil {
		fmt.Printf("  error: %v\n", err)
	} else {
		printParsed(ld)
		snap := gateway.SnapshotFromLiveData(ld)
		fmt.Printf("\n  snapshot summary:\n")
		fmt.Printf("    Solar:    %8.1f W\n", snap.SolarW)
		fmt.Printf("    Battery:  %8.1f W  (SOC %d%%, %d Wh stored)\n", snap.BatteryW, snap.BatterySOC, snap.BatteryWh)
		fmt.Printf("    Grid:     %8.1f W  (%s)\n", snap.GridW, gridDirection(snap))
		fmt.Printf("    Load:     %8.1f W\n", snap.LoadW)
		fmt.Printf("    Solar→load: %.1f W  solar→grid: %.1f W  solar→batt: %.1f W\n",
			snap.SolarToLoad, snap.SolarToGrid, snap.SolarToBatt)
		fmt.Printf("    Self-sufficiency: %.0f%%\n", snap.SelfSufficiency()*100)
	}

	// ── Meters (config) ────────────────────────────────────────────────────────
	section("Meters", "GET", "/ivp/meters")
	meters, err := client.Meters(ctx)
	if err != nil {
		fmt.Printf("  error: %v\n", err)
	} else {
		printParsed(meters)
	}

	// ── MeterReadings ──────────────────────────────────────────────────────────
	section("MeterReadings", "GET", "/ivp/meters/readings")
	readings, err := client.MeterReadings(ctx)
	if err != nil {
		if gateway.IsNotFound(err) {
			fmt.Println("  [not found — gateway has no CT metering hardware]")
		} else {
			fmt.Printf("  error: %v\n", err)
		}
	} else {
		printParsed(readings)
	}

	// ── GridReadings ───────────────────────────────────────────────────────────
	section("GridReadings", "GET", "/ivp/meters/gridReading")
	grid, err := client.GridReadings(ctx)
	if err != nil {
		fmt.Printf("  error: %v\n", err)
	} else {
		printParsed(grid)
	}

	// ── Consumption ────────────────────────────────────────────────────────────
	section("Consumption", "GET", "/ivp/meters/reports/consumption")
	cons, err := client.Consumption(ctx)
	if err != nil {
		if gateway.IsNotFound(err) {
			fmt.Println("  [not found — gateway has no CT metering hardware]")
		} else {
			fmt.Printf("  error: %v\n", err)
		}
	} else {
		printParsed(cons)
	}

	// ── Production ─────────────────────────────────────────────────────────────
	section("Production", "GET", "/api/v1/production")
	prod, err := client.Production(ctx)
	if err != nil {
		fmt.Printf("  error: %v\n", err)
	} else {
		printParsed(prod)
	}

	// ── Inverters ──────────────────────────────────────────────────────────────
	section("Inverters", "GET", "/api/v1/production/inverters")
	inverters, err := client.Inverters(ctx)
	if err != nil {
		fmt.Printf("  error: %v\n", err)
	} else {
		printParsed(inverters)
		if len(inverters) > 0 {
			fmt.Printf("\n  %d inverter(s) — last report watts (min/max):\n", len(inverters))
			for _, inv := range inverters {
				fmt.Printf("    %s  now=%dW  max=%dW\n",
					inv.SerialNumber, inv.LastReportWatts, inv.MaxReportWatts)
			}
		}
	}

	// ── Energy ─────────────────────────────────────────────────────────────────
	section("Energy", "GET", "/ivp/pdm/energy")
	energy, err := client.Energy(ctx)
	if err != nil {
		fmt.Printf("  error: %v\n", err)
	} else {
		printParsed(energy)
	}

	// ── Devices ─────────────────────────────────────────────────────────────────
	section("Devices", "GET", "/ivp/ensemble/device_list")
	devices, err := client.Devices(ctx)
	if err != nil {
		fmt.Printf("  error: %v\n", err)
	} else {
		printParsed(devices)
	}

	// ── BatteryInventory ───────────────────────────────────────────────────────
	section("BatteryInventory", "GET", "/ivp/ensemble/inventory")
	batteries, err := client.BatteryInventory(ctx)
	if err != nil {
		fmt.Printf("  error: %v\n", err)
	} else {
		printParsed(batteries)
		if len(batteries) > 0 {
			fmt.Printf("\n  %d Encharge unit(s):\n", len(batteries))
			for _, b := range batteries {
				fmt.Printf("    %s  soc=%d%%  temp=%d°C (max cell %d°C)  cap=%dWh\n",
					b.SerialNum, b.PercentFull, b.Temperature, b.MaxCellTemp, b.CapacityWh)
				fmt.Printf("             signal: sub-ghz=%d/4  2.4ghz=%d/4  fw=%s\n",
					b.CommLevelSubGHz, b.CommLevel24GHz, b.Firmware)
				fmt.Printf("             grid_mode=%s  admin=%s  communicating=%v\n",
					b.GridMode, b.AdminStateStr, b.Communicating)
			}
		}
	}

	fmt.Printf("\n%s\n  probe complete\n%s\n", rule, rule)
}

// gridDirection returns a human-readable label for the current grid flow.
func gridDirection(s gateway.EnergySnapshot) string {
	switch {
	case s.IsExporting():
		return "exporting"
	case s.IsImporting():
		return "importing"
	default:
		return "balanced"
	}
}

func fatalf(format string, args ...any) {
	fmt.Fprintf(os.Stderr, "gateway-probe: "+format+"\n", args...)
	os.Exit(1)
}
