# Changelog

All notable changes to this project are documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.0.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [v3.6.0] — 2026-07-08

### Added

- **Proxmox Datacenter Manager support**: generated typed bindings for the
  full PDM JSON API (327 method-operations, PDM 1.1.6) under `pkg/pdm/*` (`access`,
  `autoinstall`, `ceph`, `config`, `nodes`, `pbs`, `ping`, `pve`, `remotes`,
  `resources`, `sdn`, `subscriptions`, `version`), produced by
  `cmd/pvegen --dialect pdm` from the vendored `_data/pdm-apidoc.json`. The
  `/pve` and `/pbs` trees are PDM's proxied per-remote operations against
  managed instances, including cross-cluster guest migration. The
  `/auto-install` namespace is emitted as package `autoinstall` (a hyphen is
  not a legal Go package name).
- `pkg/pdm.NewClient` / `pkg/pdm.DefaultOptions`: a preset over `pkg/client`
  that applies the PDM wire defaults — port 8443, `PDMAPIToken`
  Authorization prefix, `PDMAuthCookie` ticket cookie. Everything else (TLS
  pinning, retries, logging) is the shared client, unchanged.
- `cmd/pvegen --dialect pdm` selecting the PDM spec dialect.

## [v3.5.0] — 2026-07-07

### Added

- Tolerant `client.PVEInt` type for response integer fields the upstream
  API encodes inconsistently (native number or quoted string), following
  the `PVEFloat` pattern from v3.2.5.

## [v3.4.0] — 2026-07-07

### Changed

- **Module renamed** from `github.com/fivetwenty-io/pve-apiclient-go/v3` to `github.com/fivetwenty-io/proxmox-apiclient-go/v3` — the library now covers more than one Proxmox product. Versions up to v3.3.1 remain published under the old path; see MIGRATION.md for the one-line consumer migration. The TOFU fingerprint cache moved to `~/.config/proxmox-apiclient-go/fingerprints.json` (the legacy location is still read if the new one does not exist).

### Added

- **Proxmox Backup Server support**: generated typed bindings for the full PBS JSON API (346 endpoints) under `pkg/pbs/*` (`access`, `admin`, `config`, `nodes`, `tape`, `status`, `version`, `ping`, `pull`, `push`), produced by `cmd/pvegen --dialect pbs` from the vendored `_data/pbs-apidoc.json`. The HTTP/2 chunk-protocol endpoints (`/backup`, `/reader`) are intentionally excluded.
- `pkg/pbs.NewClient` / `pkg/pbs.DefaultOptions`: a preset over `pkg/client` that applies the PBS wire defaults — port 8007, `PBSAPIToken` Authorization prefix, `PBSAuthCookie` ticket cookie. Everything else (TLS pinning, retries, logging, ticket login) is the shared client, unchanged.
- `cmd/pvegen --dialect {pve|pbs}` flag selecting spec dialect, naming overrides, and output/import roots. Spec loading was also hardened: boolean `leaf`/`allowtoken`/`optional` encodings no longer break parsing.

## [v3.2.9] — 2026-06-22

### Added

- `Client.Close()` and `HTTPClient.Close()` release the response-cache cleanup goroutine and close idle HTTP connections. `Close` is idempotent and safe to call more than once.

### Fixed

- The response cache no longer panics on a second `Close()`; the cleanup channel is now closed exactly once.
- Each `Stream` no longer leaks a background goroutine. An inert metrics collector was started per stream and never stopped when the stream was read to EOF; it produced no observable output and has been removed.
- The connection pool's active-connection counter no longer underflows when `Put` is called without a matching `Get` (or twice for one client); the counter is clamped at zero.
- A `401` carrying a static API token is no longer retried. Because API tokens cannot be re-issued by the client, the previous refresh-and-retry replayed the same rejected token; the original `401` now surfaces after a single request. Ticket-based authenticators still refresh and retry as before.
- A malformed body on a `2xx` batch response is now reported as a failure instead of being silently counted as success. An empty body (including `204 No Content`) remains a success.

### Changed

- The unexported mutex was moved off the exported `pool.Stats` and stream metrics value types into private wrappers, so callers can copy a returned snapshot without copying a lock. The exported struct fields are unchanged.
- The unused TLS `Verifier` mode machinery and its dead error sentinels were removed. The behavior is unchanged: every non-`None` `SSLVerifyMode` performs full certificate-chain and hostname verification (fail-secure), which is now documented on the type.

### Internal

- Raised hand-written package test coverage (notably `internal/ssl`, and the cloudinit, network, storage, qemu, and client bindings) with behavior-focused tests, and added regression tests for each fix above.
- Tightened the lint configuration (targeted `varnamelen` short-name allowances and a `recvcheck` exclusion for the `MarshalJSON`/`UnmarshalJSON` pair) and resolved the substantive findings. `go vet`, `staticcheck`, `golangci-lint`, and `go test -race` pass clean.

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

[Unreleased]: https://github.com/fivetwenty-io/proxmox-apiclient-go/compare/v0.1.0...HEAD
[0.1.0]: https://github.com/fivetwenty-io/proxmox-apiclient-go/releases/tag/v0.1.0
