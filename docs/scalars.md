# Decoding PVE scalars

Proxmox VE's API is Perl underneath, and Perl does not distinguish the string
`"1"` from the number `1`. What reaches the wire therefore depends on how a
given endpoint's author happened to build the hash, not on what the endpoint
documents. `_data/apidoc.json` records the documented type, so a hand-written
response struct that follows the spec will fail to decode real payloads.

The client ships three tolerant scalar types for this, in `pkg/client`:

- `client.PVEBool`
  accepts `true`/`false`, `1`/`0` (and any other number), and the strings
  `"1"`, `"0"`, `"true"`, `"false"`, `"yes"`, `"no"`, `"on"`, `"off"`, and
  `""`. Marshals back out as a native JSON boolean.

- `client.PVEInt`
  accepts a JSON number, a numeric string, and `""` for an absent value.

- `client.PVEFloat`
  accepts a JSON number, a numeric string, and `""` for an absent value.

Reach for them in any struct you decode a response into:

```go
type disk struct {
	DevPath string           `json:"devpath"`
	Size    client.PVEInt    `json:"size"`
	Mounted client.PVEBool   `json:"mounted"`
}
```

Use a pointer (`*client.PVEBool`) when you need to tell a field PVE set to
false from one it never sent. PVE omits many flags rather than sending them as
`0`, so for a set-only flag the absent case and the false case mean the same
thing; for anything else, the distinction is real and a plain value loses it.

## Fields whose declared type is not the type PVE sends

The generator emits `[]json.RawMessage` for an array-of-objects response, so
these do not surface as wrong Go types in `pkg/api`. They bite the next layer
up, where a consumer writes the decode struct by hand and takes the spec at its
word.

| Endpoint | Field | Declared | Actually sent |
|---|---|---|---|
| `GET /nodes/{node}/disks/list` | `osdid` | `integer` | `-1` as a number when the disk backs no OSD, and the OSD id as a **string** (`"0"`) when it does |
| `GET /nodes/{node}/disks/list` | `osdid-list[]` | `integer` | array of **strings** (`["0"]`) |
| `GET /cluster/firewall/groups/{group}/{pos}` | rule position | `integer` | a **string** |
| `GET /nodes/{node}/{lxc,qemu}/{vmid}/status/current` | pressure-stall (PSI) metrics | `number` | **strings** |

Both forms arrive in the same response, from the same PVE 9 node, so neither
`int64` nor `string` decodes the field on its own. `client.PVEInt` decodes both.

`cmd/pvegen`'s `returnsOverrides` table cannot fix these: it replaces a broken
`returns` schema wholesale, and `goTypeFor` collapses array-of-object items to
`json.RawMessage` regardless of what the item schema says, so an override for
these endpoints would change nothing in the emitted code. Editing
`_data/apidoc.json` is not an option either: it is PVE's own dump, replaced
whole on the next refresh. Documenting the field here is what survives both.

Add a row when you find another one, and say what the live payload contained
rather than what you inferred.
