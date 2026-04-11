# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## Commands

```bash
# Run all tests
go test ./...

# Run a single test
go test -run TestClient_LiveData

# Run tests with verbose output
go test -v ./...

# Vet and lint
go vet ./...
```

No build step — this is a library package with no `main`. No external dependencies; pure stdlib only.

## Architecture

This is a Go client library (`package gateway`) for the **Enphase IQ Gateway** — a solar/battery system controller on the local LAN. All API calls go to the local device over HTTPS with a self-signed cert (`InsecureSkipVerify` is intentional and nolinted).

### Auth flow (two-step cloud → local)

`FetchJWT` in `auth.go` orchestrates the auth:
1. POST to `enlighten.enphaseenergy.com` with credentials → gets a `session_id`
2. POST to `entrez.enphaseenergy.com` with `session_id` + gateway serial → gets a JWT
3. JWT is passed to `NewClient` and sent as `Authorization: Bearer` on every gateway request

The JWT is valid for ~1 year (system owner credentials). `ParseExpiry` in `token.go` decodes the exp claim without verifying the signature. `IsUnauthorized(err)` is the signal to re-fetch.

### Client and request plumbing

`Client` in `gateway.go` wraps an `*http.Client` with a mutex-guarded JWT. All JSON endpoints go through `doJSON()`. The one XML outlier is `SystemInfo` (`/info`), which has its own request path in `system.go` — it's the only endpoint that requires no JWT and returns XML.

The `Gateway` interface in `gateway.go` covers all methods including `BatteryInventory`, `EnableLiveStream`, and `StreamLiveData`. Embed it in test doubles or mock implementations.

### Sign convention split

Raw `LiveData` fields use Enphase's conventions (Storage: negative = charging, positive = discharging; Grid: negative = exporting). `EnergySnapshot` in `snapshot.go` normalises everything to an intuitive model (positive = supplying power to the home). `SnapshotFromLiveData` is the bridge. Power values in `LiveData`/`MeterSummary` are in **milliwatts**; `EnergySnapshot` converts to Watts.

### Streaming

`stream.go` adds push-stream support:

- `EnableLiveStream`: POST `{"enable":1}` to `/ivp/livedata/stream`, verify `sc_stream == "enabled"`.
- `StreamLiveData`: calls `EnableLiveStream`, creates a no-timeout `http.Client` (shares underlying `Transport`; a timeout would kill the long-lived connection), then opens one persistent GET to `/ivp/livedata/status`. Routes to `readSSE` if `Content-Type: text/event-stream`, else `readJSON`.
- `readJSON`: uses `json.Decoder.Decode` in a loop — handles concatenated JSON regardless of newlines (important: `bufio.Scanner.ScanLines` would block on frames with no trailing `\n`).
- `readSSE` + `processSSEBuffer`: manual byte-level SSE parsing; strips `data: ` prefix, skips `:` comments and `event:` lines.
- `ErrStopStream`: sentinel that callbacks return to stop iteration cleanly; `StreamLiveData` converts it to `nil`.
- Reconnect logic lives in the application layer (`cmd/livedata-watch`), not in the library — keeps the library simple and avoids infinite loops in tests.

### API surface by file

| File | Endpoint | Notes |
|------|----------|-------|
| `livedata.go` | `GET /ivp/livedata/status` | Best source for real-time monitoring; all sources simultaneously |
| `stream.go` | `POST /ivp/livedata/stream` + persistent `GET /ivp/livedata/status` | Push-stream; ~2 frames/second; handles SSE and concatenated JSON |
| `inventory.go` | `GET /ivp/ensemble/inventory` | Rich per-battery telemetry: SOC, temps, firmware, RF signal, grid mode |
| `readings.go` | `GET /ivp/meters/readings` | CT-based readings; 5-min refresh; 404 on non-metered gateways |
| `grid.go` | `GET /ivp/meters/gridReading` | Per-phase grid readings |
| `meters.go` | `GET /ivp/meters` | Meter configuration (enabled/disabled, measurement type) |
| `consumption.go` | `GET /ivp/meters/reports/consumption` | Consumption report with cumulative lines |
| `production.go` | `GET /api/v1/production` + `/inverters` | Per-inverter production |
| `energy.go` | `GET /ivp/pdm/energy` | Energy totals by subsystem (PCU, RGM, EIM) |
| `devices.go` | `GET /ivp/ensemble/device_list` | Battery/storage device inventory |
| `system.go` | `GET /info` | XML; no JWT; serial number + firmware version |

### Testing pattern

Tests live in `gateway_test.go` (external `_test` package). Each test spins up an `httptest.NewServer`, registers handlers via the `serve`/`serveJSON` helpers, then creates a client with `gateway.WithHTTPClient(srv.Client())` to inject the test server's HTTP client. This bypasses TLS entirely — no cert setup needed.

### Error handling

`errors.go` defines `*Error{StatusCode, Endpoint}`. Use `IsUnauthorized(err)` and `IsNotFound(err)` rather than comparing status codes directly. `IsNotFound` is expected on endpoints that require hardware that may not be present (e.g., CTs for `MeterReadings`).
