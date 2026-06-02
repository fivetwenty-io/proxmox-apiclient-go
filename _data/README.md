# PVE API Specification Data

This directory holds the upstream Proxmox VE API specification used as input
to the typed client codegen pipeline at `cmd/pvegen`. The generator emits
typed bindings for all six top-level PVE namespaces:

- `/version`  → `pkg/api/version/`
- `/access`   → `pkg/api/access/`
- `/pools`    → `pkg/api/pools/`
- `/cluster`  → `pkg/api/cluster/`
- `/storage`  → `pkg/api/clusterstorage/` (renamed to avoid clashing with the
  hand-written `pkg/api/storage/` helpers that target
  `/nodes/{node}/storage/...`)
- `/nodes`    → `pkg/api/nodes/`

## Files

- `apidoc.json` — Recursive endpoint tree extracted from PVE
  (`pve-docs/api-viewer/apidoc.js`). Root is a JSON array of node objects;
  each node has `path`, `text`, `leaf`, `info` (map of HTTP method to
  endpoint definition), and optional `children`.

  **Current pin: PVE 9.2 — 444 endpoints / 675 method-operations.**

## Provenance

The spec is sourced from the published `pve.proxmox.com/pve-docs/api-viewer/`
static asset (or an equivalent running PVE deployment). It is the same data
the upstream API viewer uses to render its documentation, so it is the
canonical machine-readable definition of the REST surface.

## Regenerating

To refresh against a newer PVE release:

1. Fetch the upstream JS bundle:

   ```sh
   curl -sSL https://pve.proxmox.com/pve-docs/api-viewer/apidoc.js \
     -o /tmp/apidoc.js
   ```

2. Extract the JSON payload. The JS file assigns the schema to
   `const apiSchema = [ ... ];` and is followed by additional JavaScript
   (the api-viewer renderer), so a line-oriented `sed` strip will not work —
   the array must be bracket-matched from the assignment to its closing `]`.
   Extract and validate it as JSON in one step:

   ```sh
   python3 - <<'PY'
   import json, re
   src = open('/tmp/apidoc.js', encoding='utf-8').read()
   start = src.index('[', src.index('const apiSchema'))
   depth = 0
   for i in range(start, len(src)):
       depth += {'[': 1, ']': -1}.get(src[i], 0)
       if depth == 0:
           end = i + 1
           break
   schema = json.loads(src[start:end])  # raises if malformed
   json.dump(schema, open('_data/apidoc.json', 'w'))
   print(f'wrote {len(schema)} top-level nodes')
   PY
   ```

   (Adjust the variable name if upstream renames `apiSchema`. The
   `json.loads` call fails loudly if the extracted text is not valid JSON,
   so a clean exit means the payload parsed.)

3. Regenerate Go bindings:

   ```sh
   make generate
   ```

4. Run the verification target to confirm the tree is idempotent:

   ```sh
   make verify-generated
   ```

5. Run the full test suite:

   ```sh
   make check
   ```

## Versioning

`apidoc.json` is treated as a vendored input. A bump to a newer PVE
spec is a deliberate, reviewed change: it produces a diff in
`pkg/api/**/*_gen.go` that callers can inspect for breaking changes.
