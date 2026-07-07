# proxmox-apiclient-go

A typed Go client for the Proxmox VE 9.x REST API.

## Installation

```bash
go get github.com/fivetwenty-io/proxmox-apiclient-go/v3
```

## Quickstart

### Authenticate with a ticket (username/password)

```go
package main

import (
    "context"
    "fmt"
    "log"

    pve "github.com/fivetwenty-io/proxmox-apiclient-go/v3/pkg/client"
    "github.com/fivetwenty-io/proxmox-apiclient-go/v3/pkg/api/version"
    "github.com/fivetwenty-io/proxmox-apiclient-go/v3/pkg/api/access"
)

func main() {
    ctx := context.Background()

    client, err := pve.NewClient(pve.Options{
        Host:      "pve.example.com",
        Username:  "root@pam",
        Password:  "secret",
        AutoLogin: true,
    })
    if err != nil {
        log.Fatal(err)
    }
    defer client.Logout()

    // Call a hand-crafted endpoint
    vsvc := version.New(client)
    ver, err := vsvc.Get(ctx)
    if err != nil {
        log.Fatal(err)
    }
    fmt.Println("PVE version:", ver.Version)

    // Call a generated namespace
    asvc := access.New(client)
    users, err := asvc.ListUsers(ctx, nil)
    if err != nil {
        log.Fatal(err)
    }
    fmt.Printf("%d user(s)\n", len(*users))
}
```

## Authentication

### Ticket (username/password)

```go
client, err := pve.NewClient(pve.Options{
    Host:      "pve.example.com",
    Username:  "root@pam",
    Password:  "secret",
    AutoLogin: true, // authenticate on first request
})
```

`AutoLogin: true` handles the `Login()` call automatically on the first API
request. Call `Login()` manually when `AutoLogin` is false (the default).

### API token

Tokens use the format `USER@REALM!TOKENID=SECRET`. Pass the full string via
`Options.Token`:

```go
client, err := pve.NewClient(pve.Options{
    Host:  "pve.example.com",
    Token: "root@pam!mytoken=xxxxxxxx-xxxx-xxxx-xxxx-xxxxxxxxxxxx",
})
```

A malformed token string is rejected at construction time.

### Two-factor authentication (TFA)

Register a `TFAHandler` before calling `Login()` to handle TOTP, Yubico, or
WebAuthn challenges:

```go
client.SetTFAHandler(myTFAHandler)
err = client.Login()
```

See `pkg/auth` for the `TFAChallenge` and `TFAResponse` types.

## WebSocket (pkg/websocket)

`pkg/websocket` provides two layers:

- **`Client`** — event-driven WebSocket client for subscribing to PVE push
  events. Supports reconnection, ping/keep-alive, and per-event handlers.

- **`ProxyClient`** — typed helper for obtaining proxy tickets from the PVE
  API and opening the resulting console/terminal WebSocket connections
  (VNC, SPICE, terminal, migration tunnel).

```go
import "github.com/fivetwenty-io/proxmox-apiclient-go/v3/pkg/websocket"

proxy, err := websocket.NewProxyClient(&websocket.ProxyConfig{
    Host:       "pve.example.com",
    Ticket:     ticket,        // PVEAuthCookie value
    CSRFToken:  csrfToken,
    HTTPClient: myHTTPDoer,
})

// Obtain a VNC session ticket for VM 100 on node "pve"
session, err := proxy.VMVNCProxy(ctx, "pve", 100)

// Open the WebSocket
conn, err := proxy.VMVNCConnect(ctx, "pve", 100, session)
defer conn.Close()

// Read/write frames
_, data, err := conn.ReadMessage()
```

Supported proxy methods: `VMVNCProxy`, `VMVNCConnect`, `VMTermProxy`,
`VMTermConnect`, `VMSpiceProxy`, `NodeVNCShell`, `NodeTermShell`,
`NodeSpiceShell`, `LXCVNCProxy`, `LXCVNCConnect`, `LXCTermProxy`,
`LXCTermConnect`, and the migration-tunnel variants.

## Generated API bindings (pkg/api/\*)

The packages under `pkg/api/` expose typed bindings for all 667 PVE 9.x
endpoints. They are generated from `_data/apidoc.json` by `cmd/pvegen` and
**must not be edited by hand** — changes will be lost when `make generate` runs.

| Package | Endpoint root | Notes |
|---|---|---|
| `pkg/api/version` | `/version` | PVE version info |
| `pkg/api/access` | `/access` | Users, roles, ACLs, TFA, tokens |
| `pkg/api/cluster` | `/cluster` | HA, ACME, firewall, replication |
| `pkg/api/clusterstorage` | `/storage` | Cluster-wide storage config |
| `pkg/api/nodes` | `/nodes` | Per-node resources, VMs, containers |
| `pkg/api/pools` | `/pools` | Resource pools |

Construct a service with the shared client:

```go
import "github.com/fivetwenty-io/proxmox-apiclient-go/v3/pkg/api/cluster"

csvc := cluster.New(client)
list, err := csvc.ListCluster(ctx)
```

Indexed parameter families (e.g. `net[n]`, `scsi[n]`) are modeled as
`map[int]string` fields and expanded correctly during form encoding.
Path parameters are URL-escaped via `url.PathEscape`.

To regenerate after updating `_data/apidoc.json`:

```bash
make generate
```

To verify the checked-in files match the spec:

```bash
make verify-generated
```

## Hand-written packages

These packages cover common use cases and are maintained separately from
the generated layer:

- `pkg/api/qemu` — VM lifecycle (create, clone, snapshot, disk management)
- `pkg/api/lxc` — Container lifecycle
- `pkg/api/network` — Node network configuration
- `pkg/api/cloudinit` — Cloud-init configuration
- `pkg/api/storage` — Node-level storage operations
- `pkg/api/tasks` — UPID task polling

## Package layering

The library is organized in three layers:

- **Generated typed bindings** (`pkg/api/version`, `pkg/api/access`,
  `pkg/api/cluster`, `pkg/api/clusterstorage`, `pkg/api/nodes`,
  `pkg/api/pools`) — the primary API. Generated from `_data/apidoc.json`,
  these cover all 667 PVE 9.x endpoints with full parameter and response
  typing. Use these when you need a specific parameter or response field, or
  compile-time-checked request/response shapes.

- **Hand-written convenience helpers** (`pkg/api/qemu`, `pkg/api/lxc`,
  `pkg/api/network`, `pkg/api/storage`, `pkg/api/tasks`,
  `pkg/api/cloudinit`) — narrower, opinionated wrappers over a subset of the
  same endpoints already covered by `pkg/api/nodes` (and, for storage
  configuration, `pkg/api/clusterstorage`). They trade full typing for a
  smaller surface built around `map[string]interface{}` and purpose-built
  structs, and are a good starting point for common lifecycle operations
  (create, start/stop, clone, snapshot, upload, task-wait).

- **Standalone utilities** (`pkg/pool`, `pkg/batch`, `pkg/stream`,
  `pkg/compatibility`) — opt-in packages not wired into `pkg/client.Client`.
  `pkg/pool` accounts for concurrent use of a single shared `*http.Client`;
  `pkg/batch` runs independent requests concurrently with retry; `pkg/stream`
  reads a response body as a sequence of decoded items; `pkg/compatibility`
  checks feature support against a parsed PVE version. Construct and use each
  directly when its specific behavior is needed.

## Error handling

`pkg/errors` defines sentinel errors for HTTP status classes:

```go
import apierrors "github.com/fivetwenty-io/proxmox-apiclient-go/v3/pkg/errors"
import "errors"

_, err := svc.GetUsers(ctx, "root@pam")
if errors.Is(err, apierrors.ErrNotFound) {
    // user does not exist
}
```

Sentinels: `ErrUnauthorized` (401), `ErrForbidden` (403), `ErrNotFound` (404),
`ErrConflict` (409), `ErrServer` (5xx).

## Logging and metrics

Toggle request logging and set retry behavior per call via context helpers:

```go
ctx = client.WithRetries(ctx, 5)
ctx = client.WithRetryDelay(ctx, 300*time.Millisecond)
ctx = client.WithLogging(ctx, true)
```

Export Prometheus-style metrics:

```go
pm := metrics.NewDefaultMetrics()
client.SetMetrics(pm)
http.HandleFunc("/metrics", func(w http.ResponseWriter, r *http.Request) {
    w.Header().Set("Content-Type", "text/plain; version=0.0.4")
    _ = pm.Export(w)
})
```

## Compatibility

Targets Proxmox VE 9.x. The generated bindings are derived from the PVE 9
API documentation schema. Hand-written packages cover common operations
against older PVE releases as well, but are not formally tested against
versions prior to 9.

## Testing

```bash
make check        # lint + vet + staticcheck + tests
make test-race    # tests with race detector enabled
make coverage     # coverage report
```

Integration tests are not yet included in the test suite. The `// +build integration`
convention is reserved for future integration test files that require a live
PVE host.

## Contributing

PRs and issues welcome. Run `make check` before submitting. Ensure generated
files are committed if `_data/apidoc.json` was changed (`make verify-generated`
will flag a mismatch in CI).

## License

See the [LICENSE](LICENSE) file.
