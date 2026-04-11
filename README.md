# enphase-gateway

A Go client for the **Enphase IQ Gateway** local REST API. Reads real-time solar production, battery state, grid flow, and per-inverter data directly from the gateway on your LAN — no cloud polling required after the initial JWT fetch.

Based on this API Doc: https://enphase.com/download/iq-gateway-local-apis-or-ui-access-using-token

```
import "github.com/hobeone/enphase-gateway"
```

Pure stdlib. No external dependencies.

## Quick start

```go
// 1. Fetch a JWT from Enphase cloud (one-time; valid ~1 year).
resp, err := gateway.FetchJWT(ctx, "you@example.com", "password", "serial123")
if err != nil {
    log.Fatal(err)
}

// 2. Create a client pointing at your local gateway.
client := gateway.NewClient("envoy.local", resp.Token)

// 3. Read real-time power flows.
live, err := client.LiveData(ctx)
if err != nil {
    log.Fatal(err)
}
snap := gateway.SnapshotFromLiveData(live)
fmt.Printf("Solar: %.0fW  Battery: %.0fW  Grid: %.0fW  Load: %.0fW  SOC: %d%%\n",
    snap.SolarW, snap.BatteryW, snap.GridW, snap.LoadW, snap.BatterySOC)
```

## Authentication

`FetchJWT` performs a two-step flow against Enphase cloud:

1. `POST enlighten.enphaseenergy.com/login/login.json` — authenticates with your Enphase account credentials and returns a `session_id`.
2. `POST entrez.enphaseenergy.com/tokens` — exchanges the `session_id` and your gateway's serial number for a JWT.

The resulting JWT is sent as `Authorization: Bearer <token>` on every gateway request. Store it; it's valid for roughly one year.

```go
resp, err := gateway.FetchJWT(ctx, username, password, serial)
// resp.Token   — the raw JWT string
// resp.Expiry() — time.Time of expiration (parsed from JWT claims)
```

To check if a token has expired and needs refreshing:

```go
if gateway.IsUnauthorized(err) {
    resp, err = gateway.FetchJWT(ctx, username, password, serial)
    client.SetJWT(resp.Token) // safe for concurrent use
}
```

The gateway serial number is visible in the Enphase app under **System > Devices > Gateway**.

## Endpoints

| Method | Endpoint | Description |
|--------|----------|-------------|
| `LiveData` | `/ivp/livedata/status` | Real-time power for all sources simultaneously (solar, battery, grid, load). Best for dashboards. |
| `MeterReadings` | `/ivp/meters/readings` | CT-based instantaneous readings with per-phase detail. Refreshes every 5 min. Requires installed CTs. |
| `GridReadings` | `/ivp/meters/gridReading` | Per-phase grid measurements. |
| `Meters` | `/ivp/meters` | Meter configuration (enabled/disabled, measurement type). |
| `Consumption` | `/ivp/meters/reports/consumption` | Net-consumption report with cumulative line totals. |
| `Production` | `/api/v1/production` | Aggregate energy totals (today / 7-day / lifetime) and current watts. Works without CTs. |
| `Inverters` | `/api/v1/production/inverters` | Per-microinverter last-reported wattage. Useful for detecting underperforming panels. |
| `Energy` | `/ivp/pdm/energy` | Energy totals broken down by meter type (PCU, RGM, EIM). |
| `BatteryInventory` | `/ivp/ensemble/inventory` | Live battery telemetry: SOC, temperatures, firmware, RF signal, grid mode. Richer than `Devices`. |
| `Devices` | `/ivp/ensemble/device_list` | Encharge battery inventory with capacity and DER index. |
| `SystemInfo` | `/info` | Hardware ID and firmware version. No auth required. Returns XML. |

## EnergySnapshot

`SnapshotFromLiveData` converts a `LiveData` response into an `EnergySnapshot` with normalised sign conventions and derived flow values:

```go
snap := gateway.SnapshotFromLiveData(live)

// Power values (Watts):
//   SolarW   >= 0           (panels generating)
//   BatteryW > 0 discharging, < 0 charging
//   GridW    > 0 importing,   < 0 exporting
//   LoadW    >= 0            (total home consumption)

snap.IsExporting()    // selling surplus to grid
snap.IsImporting()    // buying from grid
snap.IsCharging()     // battery filling
snap.IsDischarging()  // battery supplying load

snap.SelfSufficiency() // 0.0–1.0; fraction of load met without grid

// Derived flow breakdown (all >= 0):
snap.SolarToLoad   // solar going directly to home
snap.SolarToGrid   // surplus solar exported
snap.SolarToBatt   // solar charging battery
snap.GridToLoad    // load powered by grid
snap.BattToLoad    // load powered by battery
```

## Client options

```go
// Custom timeout (default: 15s).
client := gateway.NewClient("envoy.local", jwt, gateway.WithTimeout(30*time.Second))

// Inject a custom HTTP client (e.g. for testing).
client := gateway.NewClient("envoy.local", jwt, gateway.WithHTTPClient(myClient))
```

The gateway uses a self-signed TLS certificate; `InsecureSkipVerify` is set automatically and is required for local-network operation.

## Error handling

```go
data, err := client.MeterReadings(ctx)
switch {
case gateway.IsUnauthorized(err): // JWT expired — re-fetch
case gateway.IsNotFound(err):     // endpoint absent (e.g. no CTs installed)
case err != nil:                  // network error or unexpected status
}
```

## Live streaming

`StreamLiveData` enables the gateway's push stream and delivers frames to a callback as they arrive (~2 frames/second). It handles the enable POST, opens a persistent GET, and routes SSE or concatenated-JSON wire formats automatically.

```go
ctx, cancel := context.WithCancel(context.Background())
defer cancel()

for {
    err := client.StreamLiveData(ctx, func(ld gateway.LiveData) error {
        snap := gateway.SnapshotFromLiveData(ld)
        fmt.Printf("solar=%.0fW  load=%.0fW  grid=%.0fW\n",
            snap.SolarW, snap.LoadW, snap.GridW)
        return nil
    })
    if err != nil || ctx.Err() != nil {
        break
    }
    // StreamLiveData returns nil on clean EOF (gateway closed connection).
    // Reconnect immediately; the gateway re-enables the stream on the next POST.
}
```

To stop early from inside the callback, return `gateway.ErrStopStream` — `StreamLiveData` will return `nil` (not an error).

`EnableLiveStream` is also exported for callers that want to activate the push stream and poll `/ivp/livedata/status` themselves.

## livedata-watch

`cmd/livedata-watch` is a small CLI that streams (or polls) `/ivp/livedata/status` and prints a human-readable summary for each frame.

```sh
# Stream mode (default) — gateway pushes ~2 frames/second:
go run github.com/hobeone/enphase-gateway/cmd/livedata-watch -config probe_cfg.json

# Poll mode — fetch once per interval:
go run github.com/hobeone/enphase-gateway/cmd/livedata-watch -config probe_cfg.json -poll 5s

# Skip cloud auth with a pre-fetched JWT:
go run github.com/hobeone/enphase-gateway/cmd/livedata-watch -addr 192.168.1.10 -jwt eyJ...
```

Example output:

```
━━━  2026-04-11 10:40:29  ━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━
  solar            885.9 W
  battery         -943.0 W  (SOC 40%, 4099 Wh)
  grid            1768.9 W  (importing)
  load            1711.9 W
  solar→load         0.0 W  →grid: 0.0 W  →batt: 943.0 W
  self-sufficiency: 0%
```

## Integration probe

`cmd/gateway-probe` is a command-line tool that exercises every API endpoint against a real gateway and prints the full HTTP exchange — request headers, raw response body, and the parsed Go struct — for each call. It is useful for validating parsing logic against live data and for inspecting undocumented fields in gateway responses.

**Build and run:**

```sh
go run github.com/hobeone/enphase-gateway/cmd/gateway-probe -config probe.json
```

**Config file** (`probe.json`) — recommended over flags to keep credentials out of shell history:

```json
{
  "addr":     "envoy.local",
  "username": "user@example.com",
  "password": "s3cr3t",
  "serial":   "123456789012"
}
```

**Flags** (all override the config file):

| Flag | Description |
|------|-------------|
| `-config` | Path to JSON config file (default: `probe.json`) |
| `-addr` | Gateway hostname or IP |
| `-username` | Enphase account email |
| `-password` | Enphase account password |
| `-serial` | Gateway serial number |
| `-jwt` | Pre-fetched JWT; skips cloud auth entirely |

**Example output** (one endpoint shown):

```
════════════════════════════════════════════════════════
  LiveData              GET /ivp/livedata/status
════════════════════════════════════════════════════════
→ GET https://envoy.local/ivp/livedata/status
  Accept: application/json
  Authorization: [redacted]
← 200 OK  (843 bytes)
  {
    "connection": { "auth_state": "ok", ... },
    "meters": { "pv": { "agg_p_mw": 3412000 }, ... }
  }

  parsed →
  { "Connection": { "AuthState": "ok" }, "Meters": { ... } }

  snapshot summary:
    Solar:      3412.0 W
    Battery:    -800.0 W  (SOC 72%, 3600 Wh stored)
    Grid:          0.0 W  (balanced)
    Load:        2612.0 W
    Self-sufficiency: 100%
```

Endpoints that return 404 on non-metered gateways (`MeterReadings`, `Consumption`) print a bracketed note and continue rather than aborting the run.

## Requirements

- Go 1.22+
- An Enphase IQ Gateway on the local network
- Enphase account credentials and the gateway serial number (for JWT fetch)
