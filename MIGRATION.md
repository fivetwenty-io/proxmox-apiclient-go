# Migration notes

## Module rename: pve-apiclient-go → proxmox-apiclient-go

The library now covers Proxmox Backup Server (PBS) in addition to Proxmox VE,
so the module moved from `github.com/fivetwenty-io/pve-apiclient-go/v3` to
`github.com/fivetwenty-io/proxmox-apiclient-go/v3` (starting with v3.4.0).

To migrate, update `go.mod` and rewrite import paths:

```bash
go mod edit -droprequire github.com/fivetwenty-io/pve-apiclient-go/v3
go mod edit -require github.com/fivetwenty-io/proxmox-apiclient-go/v3@latest
grep -rl 'fivetwenty-io/pve-apiclient-go' --include='*.go' . \
  | xargs sed -i 's|fivetwenty-io/pve-apiclient-go|fivetwenty-io/proxmox-apiclient-go|g'
go mod tidy
```

No Go API changes accompany the rename — package names, types, and behavior
are identical. Versions up to v3.3.1 remain available under the old module
path; new development happens only under the new path.

The default TOFU fingerprint cache location moved to
`~/.config/proxmox-apiclient-go/fingerprints.json`; an existing legacy cache
at `~/.config/pve-apiclient-go/fingerprints.json` is still picked up
automatically until the new file exists.

## Existing v3 users

Within the (renamed) module path, the v3 Go API surface is unchanged.
There are no breaking changes to the hand-written packages (`pkg/api/qemu`,
`pkg/api/lxc`, `pkg/api/network`, `pkg/api/cloudinit`, `pkg/api/storage`,
`pkg/api/tasks`, `pkg/client`, `pkg/auth`, `pkg/errors`, `pkg/websocket`).

### New generated packages are additive

The packages `pkg/api/access`, `pkg/api/cluster`, `pkg/api/clusterstorage`,
`pkg/api/nodes`, and `pkg/api/pools` are new. Importing them is opt-in.
Existing code that does not import these packages is unaffected.

### Boolean form encoding changed

The form encoder now serializes `bool` fields as `1` and `0`, matching
the encoding Proxmox VE expects. Previously, the encoder produced the
strings `true` and `false`. If your code constructed form values by
hand using the string literals `"true"` or `"false"` and passed them
through the client's `Post`/`Put` methods, those calls will now send
`"true"` or `"false"` verbatim (as string parameters, not booleans).
To get the correct encoding, use `bool`-typed fields in a struct or
pass `1`/`0` as integers.

### API token format enforced at construction

`NewAPITokenAuthenticatorFromString` and `ParseAPIToken` validate the
token string format (`USER@REALM!TOKENID=SECRET`) at call time and return
an error for malformed input. Previously, a malformed token could be stored
and would produce a bad `Authorization` header on first use. Update any
code that was tolerating or working around a silent bad-header condition.

### WebSocket write concurrency

If you embedded or extended `pkg/websocket.Client` and call `Send` or
`SendText` from multiple goroutines, the serialization is now handled
internally by `writeMu`. No caller-side locking is needed or recommended.
Remove any external mutex that was guarding writes to avoid a deadlock.
