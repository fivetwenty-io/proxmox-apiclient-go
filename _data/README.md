# PVE API Specification Data

This directory holds the upstream Proxmox VE API specification used as input
to the typed client codegen pipeline at `cmd/pvegen`.

## Files

- `apidoc.json` — Recursive endpoint tree extracted from PVE 9.x
  (`pve-docs/api-viewer/apidoc.js`). Root is a JSON array of node objects;
  each node has `path`, `text`, `leaf`, `info` (map of HTTP method to
  endpoint definition), and optional `children`.

## Provenance

The spec is sourced from a running PVE 9.x deployment (or the published
`pve.proxmox.com/pve-docs/api-viewer/` static asset). It is the same data
the upstream API viewer uses to render its documentation, so it is the
canonical machine-readable definition of the REST surface.

## Regenerating

To refresh against a newer PVE release:

1. Fetch the upstream JS bundle:

   ```sh
   curl -sSL https://pve.proxmox.com/pve-docs/api-viewer/apidoc.js \
     -o /tmp/apidoc.js
   ```

2. Extract the JSON payload. The JS file assigns a literal to
   `const apiSchema = [...]` (or similar). Strip the leading JS prefix
   and trailing semicolon:

   ```sh
   sed -n 's/^var pveapi *= *//p;s/;$//' /tmp/apidoc.js \
     | python3 -c 'import json,sys; json.dump(json.loads(sys.stdin.read()), sys.stdout)' \
     > _data/apidoc.json
   ```

   (Adjust the variable name if upstream renames it. Always validate the
   result parses as JSON before committing.)

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
