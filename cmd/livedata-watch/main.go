// livedata-watch connects to an Enphase IQ Gateway and streams live power-flow
// data from /ivp/livedata/status, printing a human-readable summary for each
// frame received.  By default it uses the gateway's push-stream mode
// (POST /ivp/livedata/stream then read a continuous HTTP response); pass
// -poll <duration> to fall back to periodic polling instead.
//
// Usage:
//
//	# Stream mode (default) — gateway pushes ~1 frame/second:
//	livedata-watch -config probe_cfg.json
//
//	# Poll mode — fetch once per interval:
//	livedata-watch -config probe_cfg.json -poll 5s
//
//	# Skip cloud auth if you already have a JWT:
//	livedata-watch -addr 192.168.1.10 -jwt eyJ...
package main

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	gateway "github.com/hobeone/enphase-gateway"
)

type config struct {
	Addr     string `json:"addr"`
	Username string `json:"username"`
	Password string `json:"password"`
	Serial   string `json:"serial"`
	JWT      string `json:"jwt"`
}

func loadConfig(path string) (config, error) {
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

func main() {
	var (
		configFile = flag.String("config", "probe_cfg.json", "path to JSON config file")
		addr       = flag.String("addr", "", "gateway hostname or IP (overrides config)")
		jwtFlag    = flag.String("jwt", "", "pre-fetched JWT; skips cloud auth (overrides config)")
		poll       = flag.Duration("poll", 0, "use polling mode with this interval instead of streaming (e.g. 5s)")
	)
	flag.Parse()

	cfg, err := loadConfig(*configFile)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		fatalf("config: %v", err)
	}
	if *addr != "" {
		cfg.Addr = *addr
	}
	if *jwtFlag != "" {
		cfg.JWT = *jwtFlag
	}
	if cfg.Addr == "" {
		fatalf("-addr or config.addr is required")
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	if cfg.JWT == "" {
		if cfg.Username == "" || cfg.Password == "" || cfg.Serial == "" {
			fatalf("provide -jwt or a config file with username/password/serial")
		}
		fmt.Fprintf(os.Stderr, "fetching JWT for serial %s ...\n", cfg.Serial)
		tr, err := gateway.FetchJWT(ctx, cfg.Username, cfg.Password, cfg.Serial)
		if err != nil {
			fatalf("FetchJWT: %v", err)
		}
		cfg.JWT = tr.Token
		fmt.Fprintf(os.Stderr, "JWT obtained, expires %s\n\n", tr.Expiry().Format(time.RFC3339))
	}

	tlsTransport := &http.Transport{
		TLSClientConfig: &tls.Config{InsecureSkipVerify: true}, //nolint:gosec // gateway self-signed cert
	}
	client := gateway.NewClient(cfg.Addr, cfg.JWT,
		gateway.WithHTTPClient(&http.Client{
			Transport: tlsTransport,
			Timeout:   15 * time.Second,
		}),
	)

	if *poll > 0 {
		runPoll(ctx, client, *poll)
	} else {
		runStream(ctx, client)
	}
}

// runStream enables the gateway push stream and prints each frame as it arrives.
func runStream(ctx context.Context, client *gateway.Client) {
	fmt.Fprintln(os.Stderr, "enabling stream mode ...")
	err := client.StreamLiveData(ctx, func(ld gateway.LiveData) error {
		printFrame(ld)
		return nil
	})
	if err != nil && !errors.Is(err, context.Canceled) {
		fatalf("stream error: %v", err)
	}
}

// runPoll fetches /ivp/livedata/status on a fixed interval.
func runPoll(ctx context.Context, client *gateway.Client, interval time.Duration) {
	fmt.Fprintf(os.Stderr, "polling every %s ...\n", interval)
	for {
		ld, err := client.LiveData(ctx)
		if err != nil {
			if errors.Is(err, context.Canceled) {
				return
			}
			fmt.Fprintf(os.Stderr, "error: %v\n", err)
		} else {
			printFrame(ld)
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(interval):
		}
	}
}

func printFrame(ld gateway.LiveData) {
	snap := gateway.SnapshotFromLiveData(ld)
	fmt.Printf("━━━  %s  ━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━\n",
		snap.Timestamp.Format("2006-01-02 15:04:05"))
	fmt.Printf("  %-12s %8.1f W\n", "solar", snap.SolarW)
	fmt.Printf("  %-12s %8.1f W  (SOC %d%%, %d Wh)\n", "battery", snap.BatteryW, snap.BatterySOC, snap.BatteryWh)
	fmt.Printf("  %-12s %8.1f W  (%s)\n", "grid", snap.GridW, gridDir(snap))
	fmt.Printf("  %-12s %8.1f W\n", "load", snap.LoadW)
	fmt.Printf("  %-12s %8.1f W  →grid: %.1f W  →batt: %.1f W\n",
		"solar→load", snap.SolarToLoad, snap.SolarToGrid, snap.SolarToBatt)
	fmt.Printf("  self-sufficiency: %.0f%%\n\n", snap.SelfSufficiency()*100)
}

func gridDir(s gateway.EnergySnapshot) string {
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
	fmt.Fprintf(os.Stderr, "livedata-watch: "+format+"\n", args...)
	os.Exit(1)
}
