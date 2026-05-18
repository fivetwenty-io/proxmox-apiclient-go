// Command pvegen generates typed Go bindings for the Proxmox VE REST API.
//
// Input:  _data/apidoc.json — recursive endpoint tree extracted from the
//
//	upstream PVE api-viewer JS bundle.
//
// Output: pkg/api/<namespace>/<resource>_gen.go — one file per resource
//
//	family. Each file declares a Service interface, a service
//	struct that wraps pkg/client.Client, and a typed Go method
//	per (path, HTTP-verb) tuple under that namespace.
//
// Invocation:
//
//	go run ./cmd/pvegen --spec _data/apidoc.json --out pkg/api \
//	    --namespace version
//
// The --namespace flag may be repeated. If omitted, every namespace
// currently supported by the generator (see knownNamespaces below) is
// emitted. Namespaces not yet wired are skipped with a warning.
//
// Generation is deterministic: re-running with the same inputs produces
// byte-identical output. CI enforces this via `make verify-generated`.
package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"go/format"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// knownNamespaces lists the top-level path segments the generator can emit
// today. Adding a namespace is a deliberate act because every namespace
// needs at least one smoke test under pkg/api/<ns>/.
var knownNamespaces = map[string]bool{
	"version": true,
}

// node mirrors the structure of a single tree entry in apidoc.json.
type node struct {
	Path     string                  `json:"path"`
	Text     string                  `json:"text"`
	Leaf     int                     `json:"leaf"`
	Info     map[string]endpointInfo `json:"info"`
	Children []*node                 `json:"children,omitempty"`
}

// endpointInfo describes a single HTTP verb on an endpoint.
type endpointInfo struct {
	Method      string          `json:"method"`
	Name        string          `json:"name"`
	Description string          `json:"description"`
	Parameters  *schema         `json:"parameters,omitempty"`
	Returns     *schema         `json:"returns,omitempty"`
	AllowToken  int             `json:"allowtoken"`
	Permissions json.RawMessage `json:"permissions,omitempty"`
}

// schema is a (subset of) JSON-schema definition. We only model the bits
// the PVE spec actually uses.
type schema struct {
	Type        string             `json:"type,omitempty"`
	Description string             `json:"description,omitempty"`
	Properties  map[string]*schema `json:"properties,omitempty"`
	Items       *schema            `json:"items,omitempty"`
	// Optional is encoded as either int 0/1 or string "0"/"1" depending
	// on the endpoint; kept raw and decoded via isOptional().
	Optional json.RawMessage `json:"optional,omitempty"`
	Default  interface{}     `json:"default,omitempty"`
	Enum     []interface{}   `json:"enum,omitempty"`
	Format   json.RawMessage `json:"format,omitempty"`
	Pattern  string          `json:"pattern,omitempty"`
	// Minimum/Maximum are kept as RawMessage because the upstream spec
	// uses both numbers and quoted strings for these fields depending on
	// the endpoint. The generator does not currently emit range
	// validation, so a typed parse would only add fragility.
	Minimum              json.RawMessage `json:"minimum,omitempty"`
	Maximum              json.RawMessage `json:"maximum,omitempty"`
	AdditionalProperties json.RawMessage `json:"additionalProperties,omitempty"`
}

// endpoint is a fully resolved (path, verb) tuple ready to emit.
type endpoint struct {
	Path        string
	Verb        string
	Info        endpointInfo
	GoMethod    string
	GoNamespace string
}

func main() {
	specPath := flag.String("spec", "_data/apidoc.json", "Path to apidoc.json")
	outDir := flag.String("out", "pkg/api", "Root output directory")

	var nsList stringSlice
	flag.Var(&nsList, "namespace", "Namespace to emit (repeatable). Defaults to all known.")
	flag.Parse()

	wantNS := map[string]bool{}

	if len(nsList) == 0 {
		for ns := range knownNamespaces {
			wantNS[ns] = true
		}
	} else {
		for _, ns := range nsList {
			if !knownNamespaces[ns] {
				fmt.Fprintf(os.Stderr, "pvegen: warning: unknown namespace %q (skipped)\n", ns)

				continue
			}

			wantNS[ns] = true
		}
	}

	if len(wantNS) == 0 {
		fmt.Fprintln(os.Stderr, "pvegen: no namespaces selected; nothing to do")
		os.Exit(0)
	}

	tree, err := loadSpec(*specPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "pvegen: load spec: %v\n", err)
		os.Exit(1)
	}

	endpoints := collectEndpoints(tree)
	byNS := groupByNamespace(endpoints)

	for ns := range wantNS {
		eps := byNS[ns]
		if len(eps) == 0 {
			fmt.Fprintf(os.Stderr, "pvegen: warning: namespace %q has no endpoints in spec\n", ns)

			continue
		}

		err := emitNamespace(*outDir, ns, eps)
		if err != nil {
			fmt.Fprintf(os.Stderr, "pvegen: emit %s: %v\n", ns, err)
			os.Exit(1)
		}

		fmt.Fprintf(os.Stderr, "pvegen: wrote namespace %q (%d endpoints)\n", ns, len(eps))
	}
}

// stringSlice implements flag.Value for repeatable string flags.
type stringSlice []string

func (s *stringSlice) String() string { return strings.Join(*s, ",") }
func (s *stringSlice) Set(v string) error {
	*s = append(*s, v)

	return nil
}

// loadSpec reads apidoc.json and returns the top-level node list.
func loadSpec(path string) ([]*node, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", path, err)
	}

	var tree []*node

	err = json.Unmarshal(raw, &tree)
	if err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}

	if len(tree) == 0 {
		return nil, fmt.Errorf("spec %s is empty", path)
	}

	return tree, nil
}

// collectEndpoints walks the tree and returns one endpoint per (path, verb).
// Output is sorted by (path, verb) for deterministic generation.
func collectEndpoints(tree []*node) []endpoint {
	var (
		out  []endpoint
		walk func(*node)
	)

	walk = func(n *node) {
		if n == nil {
			return
		}

		verbs := make([]string, 0, len(n.Info))
		for v := range n.Info {
			verbs = append(verbs, v)
		}

		sort.Strings(verbs)

		for _, v := range verbs {
			info := n.Info[v]
			out = append(out, endpoint{
				Path:        n.Path,
				Verb:        v,
				Info:        info,
				GoMethod:    goMethodName(v, n.Path),
				GoNamespace: namespaceOf(n.Path),
			})
		}

		for _, c := range n.Children {
			walk(c)
		}
	}
	for _, root := range tree {
		walk(root)
	}

	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Path != out[j].Path {
			return out[i].Path < out[j].Path
		}

		return out[i].Verb < out[j].Verb
	})

	return out
}

// namespaceOf returns the top-level path segment for grouping. For
// "/version" the namespace is "version"; for "/nodes/{node}/qemu/..." it
// is "nodes". The root path "/" maps to "root".
func namespaceOf(path string) string {
	p := strings.TrimPrefix(path, "/")
	if p == "" {
		return "root"
	}

	if i := strings.Index(p, "/"); i >= 0 {
		return p[:i]
	}

	return p
}

// groupByNamespace buckets endpoints by their top-level namespace.
func groupByNamespace(eps []endpoint) map[string][]endpoint {
	out := map[string][]endpoint{}
	for _, ep := range eps {
		out[ep.GoNamespace] = append(out[ep.GoNamespace], ep)
	}

	return out
}

// goMethodName turns an (HTTP verb, path) into an exported Go method name.
//
// Rules:
//
//   - GET on a leaf without a list-style return -> "Get<resource>"
//   - GET on a non-leaf collection             -> "List<resource>"   (not used yet)
//   - POST  -> "Create<resource>"
//   - PUT   -> "Update<resource>"
//   - DELETE-> "Delete<resource>"
//
// For the SW2 milestone only /version is wired, so we only need the GET
// branch. Other branches stay deterministic for forward compatibility.
func goMethodName(verb, path string) string {
	resource := pascalFromPath(path)

	switch strings.ToUpper(verb) {
	case "GET":
		return "Get" + resource
	case "POST":
		return "Create" + resource
	case "PUT":
		return "Update" + resource
	case "DELETE":
		return "Delete" + resource
	default:
		return strings.Title(strings.ToLower(verb)) + resource //nolint:staticcheck // SA1019: stable for ASCII verbs
	}
}

// pascalFromPath returns a PascalCase identifier built from the final
// non-parameter segments of a path. "/version" -> "Version".
// "/nodes/{node}/qemu/{vmid}/status/current" -> "NodesQemuStatusCurrent".
// Path parameters in braces are dropped because they become Go method
// parameters, not part of the method name.
func pascalFromPath(path string) string {
	p := strings.TrimPrefix(path, "/")
	if p == "" {
		return "Root"
	}

	parts := strings.Split(p, "/")

	var kept []string

	for _, part := range parts {
		if strings.HasPrefix(part, "{") && strings.HasSuffix(part, "}") {
			continue
		}

		kept = append(kept, part)
	}

	var sb strings.Builder
	for _, part := range kept {
		sb.WriteString(toCamel(part, true))
	}

	if sb.Len() == 0 {
		return "Root"
	}

	return sb.String()
}

// toCamel converts a snake_case / dash-case / dotted token to camelCase
// (initialUpper controls the first-letter case).
func toCamel(s string, initialUpper bool) string {
	if s == "" {
		return ""
	}
	// Split on -, _, . — all common in PVE path segments.
	parts := splitOnAny(s, "-_.")

	var sb strings.Builder

	for i, p := range parts {
		if p == "" {
			continue
		}

		if i == 0 && !initialUpper {
			sb.WriteString(strings.ToLower(p[:1]))
			sb.WriteString(p[1:])

			continue
		}

		sb.WriteString(strings.ToUpper(p[:1]))
		sb.WriteString(p[1:])
	}

	return sb.String()
}

func splitOnAny(s, seps string) []string {
	return strings.FieldsFunc(s, func(r rune) bool {
		return strings.ContainsRune(seps, r)
	})
}

// emitNamespace writes pkg/api/<ns>/<ns>_gen.go for the given namespace.
// The current implementation hard-codes the /version layout because that
// is the only namespace approved for SW2. The dispatch hook below lets us
// add more namespaces without rewriting the entrypoint.
func emitNamespace(outRoot, ns string, eps []endpoint) error {
	switch ns {
	case "version":
		return emitVersion(outRoot, eps)
	default:
		return fmt.Errorf("namespace %q is listed in knownNamespaces but has no emitter", ns)
	}
}

// emitVersion generates pkg/api/version/version_gen.go from the /version
// endpoint definition. The /version endpoint is a parameter-less GET that
// returns a flat object — the simplest possible shape and the SW2 smoke
// target.
func emitVersion(outRoot string, eps []endpoint) error {
	// Locate the /version GET. The generator must NOT silently fall back
	// to "no endpoint emitted" if the spec changes shape — fail loud.
	var versionGet *endpoint

	for i := range eps {
		if eps[i].Path == "/version" && eps[i].Verb == "GET" {
			versionGet = &eps[i]

			break
		}
	}

	if versionGet == nil {
		return fmt.Errorf("spec missing GET /version (have %d endpoints in namespace)", len(eps))
	}

	respFields, err := renderResponseFields(versionGet.Info.Returns)
	if err != nil {
		return fmt.Errorf("render response: %w", err)
	}

	doc := strings.TrimSpace(versionGet.Info.Description)
	if doc == "" {
		doc = "API version details."
	}

	src := fmt.Sprintf(`// Code generated by cmd/pvegen. DO NOT EDIT.

// Package version exposes typed bindings for the PVE /version endpoint.
package version

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/fivetwenty-io/pve-apiclient-go/v3/pkg/client"
)

// Service exposes typed operations on the /version endpoint family.
type Service interface {
	// Get returns %s
	Get(ctx context.Context) (*VersionResponse, error)
}

// New constructs a Service backed by the given pkg/client.Client.
// The client owns authentication, TLS, retries, and logging; this
// service only translates between typed Go shapes and the raw client.
func New(c client.Client) Service {
	if c == nil {
		panic("version: New called with nil client")
	}
	return &service{c: c}
}

type service struct {
	c client.Client
}

// VersionResponse mirrors the shape returned by GET /version.
type VersionResponse struct {
%s
}

// Get implements Service.Get. It issues GET /api2/json/version with no
// parameters and decodes the typed response.
func (s *service) Get(ctx context.Context) (*VersionResponse, error) {
	if ctx == nil {
		return nil, fmt.Errorf("version.Get: ctx must not be nil")
	}
	resp, err := s.c.GetRawCtx(ctx, "/version", nil)
	if err != nil {
		return nil, fmt.Errorf("version.Get: %%w", err)
	}
	if resp == nil {
		return nil, fmt.Errorf("version.Get: nil response from client")
	}
	if resp.Data == nil {
		return nil, fmt.Errorf("version.Get: empty data in response (code=%%d)", resp.Code)
	}
	// Round-trip the decoded data through JSON so we get strict typed
	// decoding into VersionResponse instead of map[string]interface{}.
	raw, err := json.Marshal(resp.Data)
	if err != nil {
		return nil, fmt.Errorf("version.Get: re-marshal data: %%w", err)
	}
	out := &VersionResponse{}
	err = json.Unmarshal(raw, out)
	if err != nil {
		return nil, fmt.Errorf("version.Get: unmarshal data: %%w", err)
	}
	return out, nil
}
`, escapeDoc(doc), respFields)

	formatted, err := format.Source([]byte(src))
	if err != nil {
		return fmt.Errorf("gofmt generated source: %w\n--- source ---\n%s", err, src)
	}

	dir := filepath.Join(outRoot, "version")

	err = os.MkdirAll(dir, 0o755)
	if err != nil {
		return fmt.Errorf("mkdir %s: %w", dir, err)
	}

	target := filepath.Join(dir, "version_gen.go")

	err = writeIfChanged(target, formatted)
	if err != nil {
		return fmt.Errorf("write %s: %w", target, err)
	}

	return nil
}

// renderResponseFields turns a returns-schema into the body of a Go struct
// literal definition: one field per top-level property, sorted by name.
// Optional fields use pointer types so callers can distinguish absent
// from zero.
func renderResponseFields(s *schema) (string, error) {
	if s == nil || s.Type != "object" || len(s.Properties) == 0 {
		// /version always has Properties; if not, the spec changed in a
		// way that needs a human decision rather than silent emptiness.
		return "", errors.New("response schema missing object properties")
	}

	names := make([]string, 0, len(s.Properties))
	for n := range s.Properties {
		names = append(names, n)
	}

	sort.Strings(names)

	var b strings.Builder

	for _, name := range names {
		prop := s.Properties[name]

		goType, err := goTypeFor(prop)
		if err != nil {
			return "", fmt.Errorf("property %q: %w", name, err)
		}

		if isOptional(prop) {
			goType = "*" + goType
		}

		fieldName := toCamel(name, true)

		desc := strings.TrimSpace(prop.Description)
		if desc != "" {
			b.WriteString("\t// ")
			b.WriteString(fieldName)
			b.WriteString(" ")
			b.WriteString(escapeDoc(desc))
			b.WriteString("\n")
		}

		jsonTag := name
		if isOptional(prop) {
			jsonTag += ",omitempty"
		}

		fmt.Fprintf(&b, "\t%s %s `json:\"%s\"`\n", fieldName, goType, jsonTag)
	}

	return strings.TrimRight(b.String(), "\n"), nil
}

// goTypeFor maps a JSON-schema type to a Go type. The map is intentionally
// narrow: anything not represented in /version returns an error so we
// notice unsupported shapes early instead of silently emitting `any`.
func goTypeFor(s *schema) (string, error) {
	if s == nil {
		return "", errors.New("nil schema")
	}

	switch s.Type {
	case "string":
		return "string", nil
	case "integer":
		return "int64", nil
	case "number":
		return "float64", nil
	case "boolean":
		return "bool", nil
	case "array":
		if s.Items == nil {
			return "[]json.RawMessage", nil
		}

		inner, err := goTypeFor(s.Items)
		if err != nil {
			return "", fmt.Errorf("array items: %w", err)
		}

		return "[]" + inner, nil
	case "object":
		// Nested objects without an inline Go type fall back to raw JSON.
		// /version has no nested objects, so this branch is not exercised
		// today; it is here to keep the generator total.
		return "json.RawMessage", nil
	case "":
		return "json.RawMessage", nil
	default:
		return "", fmt.Errorf("unsupported schema type %q", s.Type)
	}
}

// isOptional reports whether a schema's optional flag is truthy. The
// upstream spec encodes it as either the integer 1 or the string "1";
// any other shape (absent, 0, "0", null, "") means required.
func isOptional(s *schema) bool {
	if s == nil || len(s.Optional) == 0 {
		return false
	}

	var asInt int

	err := json.Unmarshal(s.Optional, &asInt)
	if err == nil {
		return asInt == 1
	}

	var asStr string

	err = json.Unmarshal(s.Optional, &asStr)
	if err == nil {
		return asStr == "1" || strings.EqualFold(asStr, "true")
	}

	return false
}

// escapeDoc strips characters that break Go doc comments (mainly trailing
// whitespace and embedded newlines).
func escapeDoc(s string) string {
	s = strings.ReplaceAll(s, "\r\n", " ")
	s = strings.ReplaceAll(s, "\n", " ")

	return strings.TrimSpace(s)
}

// writeIfChanged writes data to path only when the existing content
// differs. This is a quality-of-life improvement for editor watchers and
// also keeps mtimes stable across no-op generator runs.
func writeIfChanged(path string, data []byte) error {
	existing, err := os.ReadFile(path)
	if err == nil && bytes.Equal(existing, data) {
		return nil
	}

	return os.WriteFile(path, data, 0o644)
}
