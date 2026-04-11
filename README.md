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

## Requirements

- Go 1.22+
- An Enphase IQ Gateway on the local network
- Enphase account credentials and the gateway serial number (for JWT fetch)
