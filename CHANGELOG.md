# Changelog

All notable changes to this project are documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.0.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [v3.2.8] — 2026-06-22

### Fixed

- Integer request parameters of 1,000,000 or greater are no longer transmitted in scientific notation. The generated bindings build the request body by marshaling the typed `*Params` struct to JSON and unmarshaling into `map[string]interface{}`; the default `encoding/json` decode turned every number into `float64`, and the form encoder's fallback (`fmt.Sprintf("%v", float64)`) rendered any value `>= 1e6` as e.g. `1.048576e+06`, which PVE rejects. This silently broke realistic calls such as `bwlimit=1048576` on backup/vzdump, all UNIX-epoch parameters (`expire`, task `since`/`until`, firewall-log `since`/`until`), and `nf-conntrack-max`. As with the booleans of v3.2.4 and numbers of v3.2.5, this completes the request-side counterpart to those response-side fixes.

### Changed

- The generator (`cmd/pvegen`) now decodes request params with a `json.Decoder` configured via `UseNumber()`, so integers reach the form encoder as `json.Number` and keep their exact digits. The form encoder (`internal/http`) gained `json.Number`, `float64`, and `float32` cases that emit plain decimal notation (never an exponent), which also hardens any hand-written body map that still decodes without `UseNumber`. Regenerated bindings change only the param-decode line in each method; no exported symbols changed and encoding of every other type is identical.

## [v3.2.5] — 2026-06-03

### Fixed

- Response numbers now decode the JSON encodings the Proxmox VE API emits. As with booleans in v3.2.4, PVE renders documented numbers inconsistently — most notably the pressure-stall (PSI) metrics on container and VM status (`pressurecpusome`, `pressureiofull`, …) arrive as JSON strings rather than numbers — which caused typed status responses (for example `ListLxcStatusCurrent`) to fail with `cannot unmarshal string into Go struct field ... of type float64` against real payloads. A new tolerant `client.PVEFloat` type accepts both the numeric and string forms (and an empty string as `0`) and marshals back out as a native JSON number.

### Changed

- The generator (`cmd/pvegen`) now emits `*client.PVEFloat` for floating-point fields in response structs; request parameter structs keep plain `float64` so query encoding is unchanged. Regenerated bindings retype 24 response float fields. `PVEFloat` has an underlying type of `float64` and a `Float()` accessor; call sites reading these fields convert with `float64(*field)`, `field.Float()`, or — for a pointer — the direct conversion `(*float64)(field)`.

## [v3.2.4] — 2026-06-03

### Fixed

- Response booleans now decode the several JSON encodings the Proxmox VE API emits. PVE renders booleans inconsistently across endpoints — as a JSON boolean (`true`/`false`), a number (`1`/`0`), or a string (`"1"`/`"0"`, `"true"`/`"false"`, `"yes"`/`"no"`, `""`) — which caused typed get-by-id responses (for example QEMU status `agent`, user `enable`, role privileges) to fail with `cannot unmarshal number into Go struct field ... of type bool` against real payloads. A new tolerant `client.PVEBool` type accepts every form and marshals back out as a native JSON boolean.

### Changed

- The generator (`cmd/pvegen`) now emits `*client.PVEBool` for boolean fields in response structs; request parameter structs keep plain `bool` so query encoding is unchanged. Regenerated bindings retype 150 response boolean fields. `PVEBool` has an underlying type of `bool` and a `Bool()` accessor; call sites reading these fields convert with `bool(*field)` or `field.Bool()`.

## [v3.2.1] — 2026-06-01

### Changed

- Wrapped WebSocket transport errors (`Conn.ReadMessage`/`WriteMessage`/`Close` and proxy POST) so failures carry package context, and promoted ad-hoc error strings to named sentinels in the streaming and generator code paths. No exported symbols changed; behavior is identical.
- Extracted repeated content-type, log-field, realm, and protocol literals into named constants; reduced the complexity of several transport, streaming, and disk-attach helpers by extracting sub-functions. Pure refactor, no API or behavior change.

### Internal

- Scoped `gosec` and `golangci-lint` to exclude generator-emitted bindings and domain-inherent rules (request structs carry credentials, deliberate certificate pinning, opt-in `InsecureSkipVerify`, and issuing HTTP to a caller-configured host), keeping the Makefile and CI in sync. The full lint and security suite (`go vet`, `staticcheck`, `golangci-lint`, `gosec`, `govulncheck`, `go test -race`) now passes clean.
- Hardened the test suite: descriptive variable names, parallel-safe subtests, closed response bodies, two-value type assertions, and deduplicated fixtures. Generated bindings remain owned by `cmd/pvegen`; none were hand-edited.

## [v3.2.0] — 2026-06-01

### Added

- Full Proxmox VE 9.2 API surface. Bindings regenerated from the official 9.2 `apidoc`. New operations: cluster-wide QEMU listing (`Cluster().ListQemu`), QEMU CPU flags (`Cluster().ListQemuCpuFlags`), custom CPU model CRUD (`Cluster().ListQemuCustomCpuModels`, `CreateQemuCustomCpuModels`, `GetQemuCustomCpuModels`, `UpdateQemuCustomCpuModels`, `DeleteQemuCustomCpuModels`), and `Nodes().DeleteCephFs`. New optional parameters for SDN fabrics, controllers, and zones (`Redistribute`, `BgpMode`, `Nodes`, `PeerGroupName`, `SecondaryControllers`) and for access domains (`Audiences`). All additions are backward compatible — no exported symbols were removed or renamed.

- `Tasks().WaitForUPID(ctx, upid, opts)`, `ParseUPID`, and a typed `UPID` struct for awaiting asynchronous PVE tasks. Task `Status.Warned` distinguishes a warning-completion (`OK: WARNINGS`) from a clean exit.

- Ordered option-string and indexed-array encoding helpers in the form encoder: `OptionString`, `NewOptionString`, `OptionStringOf`, `IndexedSlice`, and `IndexedSliceOf`. PVE option strings such as `virtio,bridge=vmbr0` and indexed array parameters such as `key0`/`key1` now serialize in the exact order PVE expects, with a positional leading token and booleans encoded as `1`/`0`.

- Ticket-only authentication via `NewTicketAuthenticatorFromTicket`. `Client.UpdateTicket` and `Client.UpdateCSRFToken` now propagate to the active authenticator.

### Fixed

- Write requests (POST, PUT, DELETE) are no longer silently auto-retried. Only idempotent methods (GET, HEAD, OPTIONS) retry automatically; opt a non-idempotent call into retry explicitly with `WithForceRetry`. This prevents duplicate side effects — for example, duplicate VM creation — when a write succeeds on the server but the response is lost to a transient failure.

- A retried request now re-buffers its body correctly via `Request.GetBody`, so second and later attempts resend the original payload instead of an empty body.

- An HTTP 401 response now forces re-authentication and a single retry of the original request.

- A 2xx response carrying a non-empty `errors` map is now surfaced as an `APIError` instead of being treated as success.

- `IsRetryableCode` no longer treats HTTP 423 (Locked) as retryable. 429, 502, 503, and 504 still retry.

### Changed

- `_data/apidoc.json` refreshed to the Proxmox VE 9.2 specification (444 endpoints / 675 method-operations).

## [v3.1.6] — 2026-05-20

### Added

- `Storage().DeleteVolumeAsync(ctx, node, storage, volume) (upid, err)` and `Storage().DeleteVolumeIfExistsAsync(ctx, node, storage, volume) (existed, upid, err)` — return the queued `imgdel` task UPID so callers can await completion via `Tasks()` before re-uploading to the same volume name. Existing `DeleteVolume` and `DeleteVolumeIfExists` are unchanged (they now delegate to the async variants and discard the UPID); their doc comments now warn that under per-storage lock contention, the queued `imgdel` task can run *after* the call returns, silently removing a subsequently-uploaded replacement. Any caller pattern of *delete-then-immediately-upload-same-name* must migrate to the async variant.

## [v3.1.5] — 2026-05-19

### Fixed

- `Cloudinit().Attach` and `Cloudinit().AttachWithNetwork` no longer send `filename` as a duplicate form field. Same root cause and fix as the `Storage().Upload` change in v3.1.4.

## [v3.1.4] — 2026-05-19

### Fixed

- `Storage().Upload` no longer sends `filename` as a duplicate form field. PVE rejects the request with HTTP 400 when `filename` appears both as a form field and as the multipart file part name; the file part already carries the destination name via its `filename` attribute.

## [v3.1.3] — 2026-05-19

### Added

- `Storage().Upload(ctx, node, storage, content, filename, body) (upid, err)` — uploads a file to a named storage pool as the given content type; returns the upload UPID for the caller to await via Tasks if synchronous semantics are required.

- `Storage().DeleteVolumeIfExists(ctx, node, storage, volume) (existed, err)` — deletes a volume and reports whether it was present; returns `(false, nil)` on 404, `(true, nil)` on success, and a wrapped error on any other failure. Distinct from `DeleteVolume`, which silently swallows 404.

## [Unreleased] — 2026-05-18

### Fixed

- **API token authentication** — the `Authorization` header was being
  constructed incorrectly. The token is now formatted as
  `PVEAPIToken=USER@REALM!TOKENID=SECRET` and validated at construction
  time; malformed tokens return an error immediately instead of silently
  producing a bad header.

- **TicketAuthenticator data race** — concurrent requests that triggered
  a ticket refresh simultaneously could race on the internal ticket field.
  All reads and writes are now guarded by `sync.RWMutex`.

- **Form encoding** — slices, booleans, and nested maps were not serialized
  correctly for PVE form-encoded POST/PUT bodies. Booleans now encode as
  `1`/`0` (not `true`/`false`), slices expand to repeated keys, and nested
  maps flatten with the dot-notation PVE expects.

- **WebSocket race conditions** — four distinct races in `pkg/websocket`:
  concurrent writes to the gorilla connection (not concurrency-safe),
  the pong handler racing with the write serializer, the read loop and
  ping loop overlapping, and a lock-order inversion in `Disconnect`.
  All four are resolved; `writeMu` now serializes every frame write.

### Added

- **Typed API bindings** — `cmd/pvegen` generates typed `Service` interfaces
  and request/response structs for all 667 PVE 9.x endpoints from
  `_data/apidoc.json`. Generated files carry `// Code generated by
  cmd/pvegen. DO NOT EDIT.` headers.

- **New generated packages**:
  - `pkg/api/access` — users, roles, ACLs, TFA, API tokens (`/access`)
  - `pkg/api/cluster` — HA, ACME, firewall, replication, SDN (`/cluster`)
  - `pkg/api/clusterstorage` — cluster-wide storage configuration (`/storage`)
  - `pkg/api/nodes` — per-node resources, VMs, containers, tasks (`/nodes`)
  - `pkg/api/pools` — resource pool management (`/pools`)

- **`pkg/websocket` ProxyClient** — typed methods for obtaining proxy tickets
  and opening console WebSocket connections: `VMVNCProxy`, `VMVNCConnect`,
  `VMTermProxy`, `VMTermConnect`, `VMSpiceProxy`, `NodeVNCShell`,
  `NodeTermShell`, `NodeSpiceShell`, `LXCVNCProxy`, `LXCVNCConnect`,
  `LXCTermProxy`, `LXCTermConnect`, and migration-tunnel variants
  (`MTunnelWebSocket`, `MTunnelWebSocketVM`).

- **Error sentinels** in `pkg/errors`: `ErrUnauthorized`, `ErrForbidden`,
  `ErrNotFound`, `ErrConflict`, `ErrServer`. All wrap `*APIError` and support
  `errors.Is` matching.

- **Make targets** `generate` and `verify-generated` for managing the
  code-generation lifecycle.

### Improved

- **Test coverage**:
  - `internal/http`: 6.6% → 87%
  - `pkg/auth`: 38.8% → 87.8%
  - `pkg/errors`: 83.8% → 97.8%
  - `pkg/api/tasks`: 68.9% → 97.3%

### Notes

- Indexed parameter families (`net[n]`, `scsi[n]`, `ide[n]`, etc.) are
  modeled as `map[int]string` in generated request types. `MarshalJSON`
  expands them in sorted key order.
- Path parameters are escaped with `url.PathEscape` in all generated
  service methods.

## [0.1.0] - TBD

### Added

- Initial alpha release
- Core functionality for PVE API interaction
- Basic documentation and examples

[Unreleased]: https://github.com/fivetwenty-io/pve-apiclient-go/compare/v0.1.0...HEAD
[0.1.0]: https://github.com/fivetwenty-io/pve-apiclient-go/releases/tag/v0.1.0
