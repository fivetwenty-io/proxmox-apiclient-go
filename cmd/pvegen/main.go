// Command pvegen generates typed Go bindings for the Proxmox VE REST API.
//
// Input:  _data/apidoc.json — recursive endpoint tree extracted from the
//
//	upstream PVE api-viewer JS bundle.
//
// Output: pkg/api/<namespace>/<namespace>_gen.go — one file per
//
//	top-level namespace. Each file declares a Service interface, a
//	concrete service struct that wraps pkg/client.Client, and a
//	typed Go method per (path, HTTP-verb) tuple under that
//	namespace. Sibling file <namespace>_smoke_test.go (see
//	testgen.go) exercises every generated method — GET, POST, PUT,
//	and DELETE alike — over a single shared httptest server per
//	package: each endpoint gets a subtest that calls it with sample
//	path/param values and asserts the recorded request's method,
//	path, and required-parameter wire encoding, plus a handful of
//	per-verb subtests asserting that a 500 response surfaces as an
//	error.
//
// Invocation:
//
//	go run ./cmd/pvegen --spec _data/apidoc.json --out pkg/api
//
// The --namespace flag may be repeated to restrict emission to a
// subset of namespaces; when omitted, every top-level namespace found
// in the spec is emitted (subject to the namespaceOutputDir override
// table below for cases where a hand-written package already owns the
// canonical directory).
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
	"regexp"
	"sort"
	"strings"
)

// File permission constants used when creating directories and output files.
const (
	dirPerm  = 0o755
	filePerm = 0o644
)

// Schema type and identifier constants used throughout the generator.
const (
	schemaTypeObject  = "object"
	schemaTypeArray   = "array"
	schemaTypeString  = "string"
	schemaTypeInteger = "integer"
	schemaTypeNumber  = "number"
	schemaTypeBoolean = "boolean"
	schemaTypeNull    = "null"
	goTypeRawMessage  = "json.RawMessage"
	verbHTTPGet       = "GET"
	verbHTTPPost      = "POST"
	verbHTTPPut       = "PUT"
	verbHTTPDelete    = "DELETE"
	identifierRoot    = "Root"
	// methodPrefixGet is both the emitted Go method prefix for GET requests
	// on a dynamic-tail path (see goMethodBaseName) and the methodNameOverrides
	// value for GET /version.
	methodPrefixGet = "Get"
	// namespaceVersion is the one top-level spec namespace whose smoke test
	// emission emitNamespace deliberately skips (see the doc comment there).
	namespaceVersion = "version"
	// namespaceRoot is the namespace namespaceOf assigns to the bare "/"
	// path (the API directory index in the PBS spec).
	namespaceRoot = "root"
)

// Go type name constants returned by goTypeFor and consumed when
// constructing sample values for generated tests (see testgen.go).
const (
	goTypeString  = "string"
	goTypeInt64   = "int64"
	goTypeFloat64 = "float64"
	goTypeBool    = "bool"
)

// Sentinel errors for well-defined failure modes within the generator.
var (
	errEmptySpec       = errors.New("spec is empty")
	errNilSchema       = errors.New("nil schema")
	errMissingObjProps = errors.New("response schema missing object properties")
)

// indexedParamRe matches PVE spec param names of the form "word[n]" or
// "word[%d]". The captured group is the base name (e.g. "net" for "net[n]").
var indexedParamRe = regexp.MustCompile(`^([a-z][a-z0-9]*)(?:\[n\]|\[%d\])$`)

// dialectConfig captures the differences between the two apidoc dialects
// the generator understands: "pve" (_data/apidoc.json → pkg/api) and
// "pbs" (_data/pbs-apidoc.json → pkg/pbs). Both trees share the same
// node/schema shape; the dialect only controls naming overrides, which
// namespaces are emitted, and where smoke tests import the generated
// package from.
type dialectConfig struct {
	// pkgImportRoot is the module-relative root the generated smoke
	// tests import the package under test from ("pkg/api" or "pkg/pbs").
	pkgImportRoot string

	// methodNameOverrides pins the emitted Go method name for specific
	// "VERB /path" tuples where the default naming rule produces an
	// awkward identifier ("ListVersion", "CreatePull", ...).
	methodNameOverrides map[string]string

	// namespaceOutputDir overrides the on-disk directory for a top-level
	// spec namespace whose default name would clash with an existing
	// hand-written package. The map key is the path-prefix segment from
	// the spec; the value is the directory under --out and also the Go
	// package name. Keep this list short — every entry is a deviation
	// from the "namespace name == directory name" convention and needs a
	// note in design.md.
	namespaceOutputDir map[string]string

	// skipNamespaces drops entire top-level namespaces from emission.
	skipNamespaces map[string]bool

	// skipSmokeTests lists namespaces whose smoke test emission is
	// suppressed because hand-written tests already own the package.
	skipSmokeTests map[string]bool
}

//nolint:gochecknoglobals // intentional package-level lookup table; consulted throughout emission
var dialects = map[string]*dialectConfig{
	"pve": {
		pkgImportRoot: "pkg/api",
		// The version package has hand-written tests that predate the
		// generator and depend on the shorter "Get" name. Keeping those
		// tests stable is cheaper than rewriting them.
		methodNameOverrides: map[string]string{
			"GET /version": methodPrefixGet,
		},
		namespaceOutputDir: map[string]string{
			// /storage top-level endpoints (datacenter storage config)
			// live in pkg/api/clusterstorage. pkg/api/storage is owned by
			// hand-written nodes/{node}/storage helpers that predate the
			// generator.
			"storage": "clusterstorage",
		},
		skipNamespaces: map[string]bool{},
		// The version package has hand-written tests already and the
		// generated smoke test must not stomp on them.
		skipSmokeTests: map[string]bool{namespaceVersion: true},
	},
	"pbs": {
		pkgImportRoot: "pkg/pbs",
		methodNameOverrides: map[string]string{
			"GET /version": methodPrefixGet,
			"GET /ping":    "Ping",
			"POST /pull":   "Pull",
			"POST /push":   "Push",
		},
		namespaceOutputDir: map[string]string{},
		skipNamespaces: map[string]bool{
			// "root" is the GET / directory index — not a usable API.
			namespaceRoot: true,
			// /backup and /reader are HTTP/2 protocol-upgrade endpoints
			// for the chunked backup stream (proxmox-backup-client
			// territory), not JSON API calls.
			"backup": true,
			"reader": true,
		},
		skipSmokeTests: map[string]bool{},
	},
}

// activeDialect is selected once in main from the --dialect flag and read
// everywhere else; the generator is single-threaded.
//
//nolint:gochecknoglobals // set once at startup, read-only afterwards
var activeDialect = dialects["pve"]

// node mirrors the structure of a single tree entry in apidoc.json.
type node struct {
	Path string `json:"path"`
	Text string `json:"text"`
	// Leaf is 0/1 in current specs but is never consumed by codegen;
	// kept raw so a dialect that encodes it as a boolean cannot break
	// spec loading.
	Leaf     json.RawMessage         `json:"leaf,omitempty"`
	Info     map[string]endpointInfo `json:"info"`
	Children []*node                 `json:"children,omitempty"`
}

// endpointInfo describes a single HTTP verb on an endpoint.
type endpointInfo struct {
	Method      string  `json:"method"`
	Name        string  `json:"name"`
	Description string  `json:"description"`
	Parameters  *schema `json:"parameters,omitempty"`
	Returns     *schema `json:"returns,omitempty"`
	// AllowToken is 0/1 in the PVE spec, absent in PBS, and never
	// consumed by codegen; kept raw for the same reason as node.Leaf.
	AllowToken  json.RawMessage `json:"allowtoken,omitempty"`
	Permissions json.RawMessage `json:"permissions,omitempty"`
}

// schema is a (subset of) JSON-schema definition. We only model the
// bits the PVE spec actually uses.
type schema struct {
	Type        string             `json:"type,omitempty"`
	Description string             `json:"description,omitempty"`
	Properties  map[string]*schema `json:"properties,omitempty"`
	Items       *schema            `json:"items,omitempty"`
	// Optional is encoded as either int 0/1 or string "0"/"1" depending
	// on the endpoint; kept raw and decoded via isOptional().
	Optional json.RawMessage `json:"optional,omitempty"`
	Default  any             `json:"default,omitempty"`
	Enum     []any           `json:"enum,omitempty"`
	Format   json.RawMessage `json:"format,omitempty"`
	Pattern  string          `json:"pattern,omitempty"`
	// Minimum/Maximum are kept as RawMessage because the upstream spec
	// uses both numbers and quoted strings for these fields. The
	// generator does not emit range validation so a typed parse would
	// add fragility for no gain.
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
	PathParams  []string // ordered, brace-stripped path-parameter names
}

func main() {
	specPath := flag.String("spec", "_data/apidoc.json", "Path to apidoc.json")
	outDir := flag.String("out", "pkg/api", "Root output directory")
	dialectName := flag.String("dialect", "pve", "Apidoc dialect: pve or pbs")

	var nsList stringSlice
	flag.Var(&nsList, "namespace", "Namespace to emit (repeatable). Defaults to every namespace in the spec.")
	flag.Parse()

	cfg, ok := dialects[*dialectName]
	if !ok {
		fmt.Fprintf(os.Stderr, "pvegen: unknown dialect %q (want pve or pbs)\n", *dialectName)
		os.Exit(1)
	}

	activeDialect = cfg

	tree, err := loadSpec(*specPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "pvegen: load spec: %v\n", err)
		os.Exit(1)
	}

	endpoints := collectEndpoints(tree)
	byNS := groupByNamespace(endpoints)
	wantNS := buildWantNS(nsList, byNS)

	if len(wantNS) == 0 {
		fmt.Fprintln(os.Stderr, "pvegen: no namespaces selected; nothing to do")
		os.Exit(0)
	}

	emitNamespaces(*outDir, wantNS, byNS)
}

// buildWantNS resolves the set of namespaces to emit from the flag list and
// the namespaces present in the spec. When nsList is empty every namespace in
// byNS is selected.
func buildWantNS(nsList stringSlice, byNS map[string][]endpoint) map[string]bool {
	wantNS := map[string]bool{}

	if len(nsList) == 0 {
		for namespace := range byNS {
			wantNS[namespace] = true
		}

		return wantNS
	}

	for _, namespace := range nsList {
		if _, ok := byNS[namespace]; !ok {
			fmt.Fprintf(os.Stderr, "pvegen: warning: namespace %q not found in spec (skipped)\n", namespace)

			continue
		}

		wantNS[namespace] = true
	}

	return wantNS
}

// emitNamespaces iterates over wantNS in deterministic order and calls
// emitNamespace for each one, exiting on first failure.
func emitNamespaces(outDir string, wantNS map[string]bool, byNS map[string][]endpoint) {
	order := make([]string, 0, len(wantNS))
	for namespace := range wantNS {
		order = append(order, namespace)
	}

	sort.Strings(order)

	for _, namespace := range order {
		eps := byNS[namespace]
		if len(eps) == 0 {
			fmt.Fprintf(os.Stderr, "pvegen: warning: namespace %q has no endpoints in spec\n", namespace)

			continue
		}

		err := emitNamespace(outDir, namespace, eps)
		if err != nil {
			fmt.Fprintf(os.Stderr, "pvegen: emit %s: %v\n", namespace, err)
			os.Exit(1)
		}

		fmt.Fprintf(os.Stderr, "pvegen: wrote namespace %q (%d endpoints)\n", namespace, len(eps))
	}
}

// stringSlice implements flag.Value for repeatable string flags.
type stringSlice []string

func (slice *stringSlice) String() string { return strings.Join(*slice, ",") }
func (slice *stringSlice) Set(val string) error {
	*slice = append(*slice, val)

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
		return nil, fmt.Errorf("%w: %s", errEmptySpec, path)
	}

	return tree, nil
}

// collectEndpoints walks the tree and returns one endpoint per
// (path, verb). Output is sorted by (path, verb) for deterministic
// generation.
func collectEndpoints(tree []*node) []endpoint {
	var (
		out  []endpoint
		walk func(*node)
	)

	walk = func(treeNode *node) {
		if treeNode == nil {
			return
		}

		verbs := make([]string, 0, len(treeNode.Info))
		for verb := range treeNode.Info {
			verbs = append(verbs, verb)
		}

		sort.Strings(verbs)

		for _, verb := range verbs {
			info := treeNode.Info[verb]
			out = append(out, endpoint{
				Path:        treeNode.Path,
				Verb:        verb,
				Info:        info,
				GoNamespace: namespaceOf(treeNode.Path),
				PathParams:  extractPathParams(treeNode.Path),
			})
		}

		for _, child := range treeNode.Children {
			walk(child)
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
// "/version" the namespace is "version"; for "/nodes/{node}/qemu/..."
// it is "nodes". The root path "/" maps to "root".
func namespaceOf(path string) string {
	trimmed := strings.TrimPrefix(path, "/")
	if trimmed == "" {
		return namespaceRoot
	}

	if idx := strings.Index(trimmed, "/"); idx >= 0 {
		return trimmed[:idx]
	}

	return trimmed
}

// groupByNamespace buckets endpoints by their top-level namespace,
// dropping namespaces the active dialect excludes from emission.
func groupByNamespace(eps []endpoint) map[string][]endpoint {
	out := map[string][]endpoint{}

	for _, endpt := range eps {
		if activeDialect.skipNamespaces[endpt.GoNamespace] {
			continue
		}

		out[endpt.GoNamespace] = append(out[endpt.GoNamespace], endpt)
	}

	return out
}

// extractPathParams returns the ordered list of {placeholder} names in
// the path, brace-stripped. "/nodes/{node}/qemu/{vmid}/status" yields
// ["node", "vmid"].
func extractPathParams(path string) []string {
	parts := strings.Split(strings.TrimPrefix(path, "/"), "/")

	var out []string

	for _, part := range parts {
		if strings.HasPrefix(part, "{") && strings.HasSuffix(part, "}") {
			out = append(out, part[1:len(part)-1])
		}
	}

	return out
}

// goMethodBaseName turns an (HTTP verb, path) into the unqualified
// PascalCase identifier for the generated method. Collisions inside
// the same namespace are resolved by the caller (see assignMethodNames).
//
// Rules:
//
//   - GET    on a path whose last segment is dynamic ("{xxx}") → "Get..."
//   - GET    on a non-dynamic path                           → "List..." when the spec marks a non-leaf, "Get..." otherwise
//   - POST   → "Create..."
//   - PUT    → "Update..."
//   - DELETE → "Delete..."
//
// The resource portion is the full PascalCase path with brace
// placeholders dropped. The full path is used (rather than just the
// trailing segment) because the spec has many endpoints whose trailing
// segment alone ("config", "status", "current") is non-unique within a
// namespace.
func goMethodBaseName(endpt endpoint) string {
	resource := pascalFromPath(endpt.Path)

	var prefix string

	switch strings.ToUpper(endpt.Verb) {
	case verbHTTPGet:
		if endsInPathParam(endpt.Path) {
			prefix = methodPrefixGet
		} else {
			prefix = "List"
		}
	case verbHTTPPost:
		prefix = "Create"
	case verbHTTPPut:
		prefix = "Update"
	case verbHTTPDelete:
		prefix = "Delete"
	default:
		prefix = pascalize(strings.ToLower(endpt.Verb))
	}

	// Drop the leading namespace segment from the resource so the
	// emitted name reads naturally ("ListUsers", not "ListAccessUsers"
	// inside package access). We keep the namespace prefix only when
	// stripping it would yield an empty identifier.
	ns := pascalize(endpt.GoNamespace)
	if ns != "" && strings.HasPrefix(resource, ns) {
		stripped := strings.TrimPrefix(resource, ns)
		if stripped != "" {
			resource = stripped
		}
	}

	if resource == "" {
		resource = identifierRoot
	}

	return prefix + resource
}

// endsInPathParam reports whether the trailing path segment is a brace
// placeholder. Used to distinguish singular-resource GETs (return a
// single item, named GetX) from collection GETs (return a list, named
// ListX).
func endsInPathParam(path string) bool {
	trimmed := strings.TrimSuffix(path, "/")
	if trimmed == "" {
		return false
	}

	idx := strings.LastIndex(trimmed, "/")
	last := trimmed[idx+1:]

	return strings.HasPrefix(last, "{") && strings.HasSuffix(last, "}")
}

// assignMethodNames mutates eps in place, filling endpoint.GoMethod
// with collision-free identifiers. Endpoints are sorted by (path,
// verb) on entry, so the order of disambiguation is stable.
func assignMethodNames(eps []endpoint) {
	seen := map[string]int{}

	for idx := range eps {
		key := strings.ToUpper(eps[idx].Verb) + " " + eps[idx].Path
		if override, ok := activeDialect.methodNameOverrides[key]; ok {
			eps[idx].GoMethod = override
			seen[override]++

			continue
		}

		base := goMethodBaseName(eps[idx])
		name := base

		if count, ok := seen[base]; ok {
			// Append a 2-based numeric suffix to keep the first hit
			// unsuffixed. This is deterministic given the input order.
			name = fmt.Sprintf("%s%d", base, count+1)
			seen[base] = count + 1
		} else {
			seen[base] = 1
		}

		eps[idx].GoMethod = name
	}
}

// pascalFromPath returns a PascalCase identifier built from the
// non-parameter segments of a path. "/version" → "Version".
// "/nodes/{node}/qemu/{vmid}/status/current" → "NodesQemuStatusCurrent".
// Brace placeholders are dropped because they become Go method
// parameters, not part of the method name.
func pascalFromPath(path string) string {
	trimmed := strings.TrimPrefix(path, "/")
	if trimmed == "" {
		return identifierRoot
	}

	parts := strings.Split(trimmed, "/")

	var kept []string

	for _, part := range parts {
		if strings.HasPrefix(part, "{") && strings.HasSuffix(part, "}") {
			continue
		}

		kept = append(kept, part)
	}

	var builder strings.Builder
	for _, part := range kept {
		builder.WriteString(pascalize(part))
	}

	if builder.Len() == 0 {
		return identifierRoot
	}

	return builder.String()
}

// pascalize converts a snake_case / dash-case / dotted token to
// PascalCase. Empty input yields "". Non-identifier characters such
// as brackets ("link[n]") are stripped before splitting; the caller
// keeps the original spelling for the JSON tag.
func pascalize(token string) string {
	if token == "" {
		return ""
	}

	token = sanitizeIdentChars(token)

	parts := splitOnAny(token, "-_.")

	var builder strings.Builder

	for _, part := range parts {
		if part == "" {
			continue
		}

		builder.WriteString(strings.ToUpper(part[:1]))
		builder.WriteString(part[1:])
	}

	return builder.String()
}

// camelize converts a snake_case / dash-case / dotted token to
// camelCase (first letter lower).
func camelize(token string) string {
	pc := pascalize(token)
	if pc == "" {
		return ""
	}

	return strings.ToLower(pc[:1]) + pc[1:]
}

// goIdentSafe returns ident if it is a non-reserved Go identifier, else
// returns ident with a trailing underscore so it does not collide with a
// keyword or predeclared name.
func goIdentSafe(ident string) string {
	switch ident {
	case "type", "range", "func", "default", "select", "chan", "map",
		"interface", "struct", "package", "import", "return", "if",
		"else", "for", "switch", "case", "break", "continue", "goto",
		"defer", "go", "const", "var", "fallthrough":
		return ident + "_"
	}

	return ident
}

func splitOnAny(str, seps string) []string {
	return strings.FieldsFunc(str, func(ch rune) bool {
		return strings.ContainsRune(seps, ch)
	})
}

// sanitizeIdentChars drops every rune that is not a letter, digit, or
// underscore. The PVE spec uses bracket suffixes like "link[n]" or
// "ip[%d]" to denote indexed arrays; those are not legal Go
// identifiers and must be stripped before Pascal-casing.
func sanitizeIdentChars(str string) string {
	var builder strings.Builder

	for _, char := range str {
		switch {
		case char >= 'a' && char <= 'z':
			builder.WriteRune(char)
		case char >= 'A' && char <= 'Z':
			builder.WriteRune(char)
		case char >= '0' && char <= '9':
			builder.WriteRune(char)
		case char == '_' || char == '-' || char == '.':
			builder.WriteRune(char)
		}
	}

	return builder.String()
}

// emitNamespace writes both the generated source and the smoke test
// for the given namespace.
func emitNamespace(outRoot, nsName string, eps []endpoint) error {
	// Sort and assign collision-free method names before emission.
	sort.SliceStable(eps, func(i, j int) bool {
		if eps[i].Path != eps[j].Path {
			return eps[i].Path < eps[j].Path
		}

		return eps[i].Verb < eps[j].Verb
	})
	assignMethodNames(eps)

	dirName := nsName
	if override, ok := activeDialect.namespaceOutputDir[nsName]; ok {
		dirName = override
	}

	pkgName := dirName
	dir := filepath.Join(outRoot, dirName)

	err := os.MkdirAll(dir, dirPerm)
	if err != nil {
		return fmt.Errorf("mkdir %s: %w", dir, err)
	}

	err = emitNamespaceSource(dir, dirName, pkgName, nsName, eps)
	if err != nil {
		return err
	}

	// Smoke tests are skipped for namespaces the dialect marks as owned
	// by hand-written tests (PVE's version package) so the generator
	// never stomps on them.
	if !activeDialect.skipSmokeTests[nsName] {
		err = emitNamespaceSmokeTest(dir, dirName, pkgName, nsName, eps)
		if err != nil {
			return err
		}
	}

	return nil
}

// emitNamespaceSource renders, formats, and writes the _gen.go file for a namespace.
func emitNamespaceSource(dir, dirName, pkgName, nsName string, eps []endpoint) error {
	src, err := renderNamespaceSource(pkgName, nsName, eps)
	if err != nil {
		return fmt.Errorf("render source: %w", err)
	}

	formatted, err := format.Source(src)
	if err != nil {
		return fmt.Errorf("gofmt generated source: %w\n--- source ---\n%s", err, src)
	}

	target := filepath.Join(dir, dirName+"_gen.go")

	err = writeIfChanged(target, formatted)
	if err != nil {
		return fmt.Errorf("write %s: %w", target, err)
	}

	return nil
}

// emitNamespaceSmokeTest renders, formats, and writes the _smoke_test.go file for a namespace.
func emitNamespaceSmokeTest(dir, dirName, pkgName, nsName string, eps []endpoint) error {
	smokeSrc := renderNamespaceSmokeTest(pkgName, nsName, eps)

	smokeFormatted, err := format.Source(smokeSrc)
	if err != nil {
		return fmt.Errorf("gofmt smoke test: %w\n--- source ---\n%s", err, smokeSrc)
	}

	smokeTarget := filepath.Join(dir, dirName+"_smoke_test.go")

	err = writeIfChanged(smokeTarget, smokeFormatted)
	if err != nil {
		return fmt.Errorf("write %s: %w", smokeTarget, err)
	}

	return nil
}

// renderNamespaceSourceHeader writes the package declaration and import block
// into builder.
func renderNamespaceSourceHeader(builder *strings.Builder, pkgName, ns string) {
	fmt.Fprintf(builder, "// Code generated by cmd/pvegen. DO NOT EDIT.\n\n")
	fmt.Fprintf(builder, "// Package %s exposes typed bindings for the PVE /%s endpoint family.\n", pkgName, ns)
	fmt.Fprintf(builder, "package %s\n\n", pkgName)
	builder.WriteString("import (\n")
	builder.WriteString("\t\"context\"\n")
	builder.WriteString("\t\"encoding/json\"\n")
	builder.WriteString("\t\"fmt\"\n")
	builder.WriteString("\t\"net/url\"\n")
	builder.WriteString("\t\"sort\"\n")
	builder.WriteString("\t\"strconv\"\n")
	builder.WriteString("\t\"strings\"\n\n")
	builder.WriteString("\t\"github.com/fivetwenty-io/proxmox-apiclient-go/v3/pkg/client\"\n")
	builder.WriteString(")\n\n")

	// Suppress "imported and not used" when a namespace happens to use
	// no Params (and therefore no strings). All current namespaces use
	// at least one Params struct, but keep a defensive blank reference
	// so the file always compiles even if a future spec shrinks.
	builder.WriteString("var _ = strings.TrimPrefix\n")
	builder.WriteString("var _ = json.RawMessage(nil)\n")
	builder.WriteString("var _ = url.PathEscape\n")
	builder.WriteString("var _ = sort.Ints\n")
	builder.WriteString("var _ = strconv.Itoa\n\n")
}

// renderNamespaceServiceInterface writes the Service interface declaration
// into builder.
func renderNamespaceServiceInterface(builder *strings.Builder, ns string, eps []endpoint) {
	builder.WriteString("// Service exposes typed operations on the /" + ns + " endpoint family.\n")
	builder.WriteString("type Service interface {\n")

	for _, endpt := range eps {
		sig := renderMethodSignature(endpt, true)
		fmt.Fprintf(builder, "\t// %s %s %s\n", endpt.GoMethod, endpt.Verb, endpt.Path)

		desc := escapeDoc(endpt.Info.Description)
		if desc != "" {
			fmt.Fprintf(builder, "\t// %s\n", desc)
		}

		fmt.Fprintf(builder, "\t%s\n", sig)
	}

	builder.WriteString("}\n\n")
}

// renderNamespaceSource builds the full _gen.go source for a namespace.
func renderNamespaceSource(pkgName, ns string, eps []endpoint) ([]byte, error) {
	var builder strings.Builder

	renderNamespaceSourceHeader(&builder, pkgName, ns)
	renderNamespaceServiceInterface(&builder, ns, eps)

	// Constructor + concrete service.
	fmt.Fprintf(&builder, `// New constructs a Service backed by the given pkg/client.Client.
// The client owns authentication, TLS, retries, and logging; this
// service only translates between typed Go shapes and the raw client.
func New(c client.Client) Service {
	if c == nil {
		panic("%s: New called with nil client")
	}
	return &service{c: c}
}

type service struct {
	c client.Client
}
`, pkgName)

	builder.WriteString("\n")

	// Per-endpoint: Params struct, Response struct, method impl.
	for _, endpt := range eps {
		err := renderEndpoint(&builder, pkgName, endpt)
		if err != nil {
			return nil, fmt.Errorf("endpoint %s %s: %w", endpt.Verb, endpt.Path, err)
		}
	}

	return []byte(builder.String()), nil
}

// renderMethodSignature returns the Go method signature line.
// When forInterface is true the receiver portion is omitted (interface
// method form); when false it includes "(s *service)".
func renderMethodSignature(endpt endpoint, forInterface bool) string {
	var args []string

	args = append(args, "ctx context.Context")
	for _, pathParam := range endpt.PathParams {
		args = append(args, goIdentSafe(camelize(pathParam))+" string")
	}

	hasParams := hasNonPathParams(endpt)
	if hasParams {
		args = append(args, fmt.Sprintf("params *%sParams", endpt.GoMethod))
	}

	respType := responseGoType(endpt)

	var retSig string

	if respType == "" {
		retSig = "error"
	} else {
		retSig = fmt.Sprintf("(*%s, error)", responseTypeName(endpt))
	}

	if forInterface {
		return fmt.Sprintf("%s(%s) %s", endpt.GoMethod, strings.Join(args, ", "), retSig)
	}

	return fmt.Sprintf("(s *service) %s(%s) %s", endpt.GoMethod, strings.Join(args, ", "), retSig)
}

// hasNonPathParams reports whether the endpoint accepts at least one
// non-path query/body parameter. Used to decide whether to emit a
// <Method>Params struct.
func hasNonPathParams(endpt endpoint) bool {
	if endpt.Info.Parameters == nil || len(endpt.Info.Parameters.Properties) == 0 {
		return false
	}

	pathSet := map[string]bool{}
	for _, pathParam := range endpt.PathParams {
		pathSet[pathParam] = true
	}

	for name := range endpt.Info.Parameters.Properties {
		if !pathSet[name] {
			return true
		}
	}

	return false
}

// responseGoType returns the Go type of the response body, or "" when
// the spec declares no return value (e.g. type:null). For object
// returns the typed struct name is returned; the actual struct is
// emitted by renderEndpoint.
func responseGoType(endpt endpoint) string {
	ret := endpt.Info.Returns
	if ret == nil {
		return ""
	}

	if ret.Type == schemaTypeNull {
		return ""
	}

	// For arrays, objects, scalars: there is always something to
	// decode; even a bare scalar returns a typed wrapper.
	return responseTypeName(endpt)
}

// responseTypeName returns the name of the Go type used for the
// endpoint's response. Currently every endpoint with a non-null
// return gets a "<Method>Response" alias for documentation purposes,
// even when the underlying shape is json.RawMessage.
func responseTypeName(endpt endpoint) string {
	return endpt.GoMethod + "Response"
}

// renderEndpoint emits the Params struct, Response type, and method
// implementation for a single endpoint.
func renderEndpoint(builder *strings.Builder, pkgName string, endpt endpoint) error {
	// Params struct.
	if hasNonPathParams(endpt) {
		err := renderParamsStruct(builder, endpt)
		if err != nil {
			return fmt.Errorf("render params: %w", err)
		}
	}

	// Response type.
	respGo := responseGoType(endpt)
	if respGo != "" {
		renderResponseType(builder, endpt)
	}

	// Method body.
	return renderMethodBody(builder, pkgName, endpt)
}

// indexedField carries metadata for a single "base[n]" param family
// found within a Params struct.
type indexedField struct {
	// BaseName is the raw spec base, e.g. "net" for "net[n]".
	BaseName string
	// FieldName is the exported Go field name, e.g. "Net".
	FieldName string
	// Desc is the doc comment text (first occurrence in the spec wins).
	Desc string
}

// pendingIndexed holds intermediate data for an indexed param before emission.
type pendingIndexed struct {
	base      string
	fieldName string
	desc      string
}

// buildIndexedPending collects pending indexed fields from the sorted parameter
// names, skipping duplicates and applying clash guards.
func buildIndexedPending(names []string, endpt endpoint, regularFieldNames map[string]bool) []pendingIndexed {
	emittedIndexed := map[string]bool{}

	var pending []pendingIndexed

	for _, name := range names {
		prop := endpt.Info.Parameters.Properties[name]

		base, ok := isIndexedParam(name)
		if !ok {
			continue
		}

		if emittedIndexed[base] {
			continue
		}

		emittedIndexed[base] = true

		fieldName := sanitizeFieldName(pascalize(base))

		// Clash guard: if the base name produces the same Go identifier
		// as a regular field (e.g. "numa" and "numa[n]" both → "Numa"),
		// append "Map" to the indexed field name so both can coexist.
		if regularFieldNames[fieldName] {
			fieldName += "Map"
		}

		desc := strings.TrimSpace(prop.Description)
		pending = append(pending, pendingIndexed{base: base, fieldName: fieldName, desc: desc})
	}

	return pending
}

// emitRegularFields writes the non-indexed struct fields into builder.
func emitRegularFields(builder *strings.Builder, names []string, endpt endpoint) error {
	for _, name := range names {
		if _, ok := isIndexedParam(name); ok {
			continue
		}

		prop := endpt.Info.Parameters.Properties[name]

		goType, err := goTypeFor(prop)
		if err != nil {
			// Fall back to json.RawMessage rather than failing
			// generation; the spec uses several shapes we deliberately
			// do not model.
			goType = goTypeRawMessage
		}

		opt := isOptional(prop)
		// Every non-path parameter is generated as a pointer so callers
		// can leave it unset. Scalars use *T; collection types keep
		// their zero-value semantics (nil map / nil slice) and are NOT
		// pointer-wrapped.
		if opt && !isAlreadyNilable(goType) {
			goType = "*" + goType
		}

		fieldName := sanitizeFieldName(pascalize(name))

		desc := strings.TrimSpace(prop.Description)
		if desc != "" {
			fmt.Fprintf(builder, "\t// %s %s\n", fieldName, escapeDoc(desc))
		}

		jsonTag := name
		if opt {
			jsonTag += ",omitempty"
		}

		fmt.Fprintf(builder, "\t%s %s `json:\"%s\"`\n", fieldName, goType, jsonTag)
	}

	return nil
}

// emitIndexedFields writes the indexed (map[int]string) struct fields and
// returns the collected indexedField metadata for MarshalJSON generation.
func emitIndexedFields(builder *strings.Builder, pending []pendingIndexed) []indexedField {
	indexedFields := make([]indexedField, 0, len(pending))

	for _, pendItem := range pending {
		if pendItem.desc != "" {
			fmt.Fprintf(builder, "\t// %s (indexed) %s\n", pendItem.fieldName, escapeDoc(pendItem.desc))
		} else {
			fmt.Fprintf(builder, "\t// %s indexed Proxmox param family (e.g. %s0, %s1, ...).\n",
				pendItem.fieldName, pendItem.base, pendItem.base)
		}

		// map[int]string is nilable; no pointer needed.
		fmt.Fprintf(builder, "\t%s map[int]string `json:\"-\"`\n", pendItem.fieldName)

		indexedFields = append(indexedFields, indexedField{
			BaseName:  pendItem.base,
			FieldName: pendItem.fieldName,
			Desc:      pendItem.desc,
		})
	}

	return indexedFields
}

// renderParamsStruct emits "type <Method>Params struct { ... }" and,
// when the endpoint has any indexed ("base[n]") parameters, a custom
// MarshalJSON method that expands map[int]string fields into numbered
// keys ("net0", "net1", …) so the existing JSON-round-trip body
// construction produces correct Proxmox wire format.
func renderParamsStruct(builder *strings.Builder, endpt endpoint) error {
	fmt.Fprintf(builder, "// %sParams is the request payload for %s.\n", endpt.GoMethod, endpt.GoMethod)
	fmt.Fprintf(builder, "type %sParams struct {\n", endpt.GoMethod)

	pathSet := map[string]bool{}
	for _, pathParam := range endpt.PathParams {
		pathSet[pathParam] = true
	}

	names := make([]string, 0, len(endpt.Info.Parameters.Properties))
	for name := range endpt.Info.Parameters.Properties {
		if pathSet[name] {
			continue
		}

		names = append(names, name)
	}

	sort.Strings(names)

	// First pass: collect all regular (non-indexed) field names so we can
	// detect clashes when assigning indexed field names.
	regularFieldNames := map[string]bool{}

	for _, name := range names {
		if _, ok := isIndexedParam(name); ok {
			continue
		}

		fn := sanitizeFieldName(pascalize(name))
		regularFieldNames[fn] = true
	}

	pending := buildIndexedPending(names, endpt, regularFieldNames)

	// Emit regular fields.
	err := emitRegularFields(builder, names, endpt)
	if err != nil {
		return err
	}

	// Emit indexed fields (map[int]string, tagged json:"-").
	indexedFields := emitIndexedFields(builder, pending)

	builder.WriteString("}\n\n")

	// If any indexed fields were emitted, generate a MarshalJSON method
	// that merges normally-tagged fields with the expanded indexed fields.
	if len(indexedFields) > 0 {
		renderIndexedMarshalJSON(builder, endpt.GoMethod, indexedFields)
	}

	return nil
}

// renderIndexedMarshalJSON emits a MarshalJSON method on the Params
// struct that:
//  1. Marshals the struct normally (indexed fields carry json:"-" so they
//     are excluded from the default encoder).
//  2. Decodes the result into a map[string]json.RawMessage.
//  3. For each indexed field with a non-nil map, inserts numbered keys
//     (e.g. "net0", "net1") sorted by index for deterministic wire output.
//  4. Re-encodes and returns the merged map.
func renderIndexedMarshalJSON(builder *strings.Builder, methodName string, fields []indexedField) {
	typeName := methodName + "Params"

	fmt.Fprintf(builder, "// MarshalJSON implements json.Marshaler for %s.\n", typeName)
	fmt.Fprintf(builder, "// It expands indexed map[int]string fields into numbered Proxmox\n")
	fmt.Fprintf(builder, "// param keys (e.g. Net[0] → \"net0\", Net[1] → \"net1\").\n")
	fmt.Fprintf(builder, "func (p %s) MarshalJSON() ([]byte, error) {\n", typeName)

	// Use an alias type to call the default encoder without recursion.
	fmt.Fprintf(builder, "\ttype alias %s\n", typeName)
	fmt.Fprintf(builder, "\tbase, err := json.Marshal(alias(p))\n")
	builder.WriteString("\tif err != nil {\n")
	fmt.Fprintf(builder, "\t\treturn nil, fmt.Errorf(\"%s.MarshalJSON: %%w\", err)\n", typeName)
	builder.WriteString("\t}\n\n")

	builder.WriteString("\tvar merged map[string]json.RawMessage\n")
	builder.WriteString("\tif err = json.Unmarshal(base, &merged); err != nil {\n")
	fmt.Fprintf(builder, "\t\treturn nil, fmt.Errorf(\"%s.MarshalJSON decode base: %%w\", err)\n", typeName)
	builder.WriteString("\t}\n")
	builder.WriteString("\tif merged == nil {\n")
	builder.WriteString("\t\tmerged = make(map[string]json.RawMessage)\n")
	builder.WriteString("\t}\n\n")

	for _, field := range fields {
		fmt.Fprintf(builder, "\t// Expand %s (map[int]string → %s0, %s1, …).\n", field.FieldName, field.BaseName, field.BaseName)
		fmt.Fprintf(builder, "\tif len(p.%s) > 0 {\n", field.FieldName)
		fmt.Fprintf(builder, "\t\tkeys%s := make([]int, 0, len(p.%s))\n", field.FieldName, field.FieldName)
		fmt.Fprintf(builder, "\t\tfor idx := range p.%s {\n", field.FieldName)
		fmt.Fprintf(builder, "\t\t\tkeys%s = append(keys%s, idx)\n", field.FieldName, field.FieldName)
		builder.WriteString("\t\t}\n")
		fmt.Fprintf(builder, "\t\tsort.Ints(keys%s)\n", field.FieldName)
		fmt.Fprintf(builder, "\t\tfor _, idx := range keys%s {\n", field.FieldName)
		fmt.Fprintf(builder, "\t\t\tkey := %s + strconv.Itoa(idx)\n", strconvQuote(field.BaseName))
		fmt.Fprintf(builder, "\t\t\tval, merr := json.Marshal(p.%s[idx])\n", field.FieldName)
		builder.WriteString("\t\t\tif merr != nil {\n")
		fmt.Fprintf(builder, "\t\t\t\treturn nil, fmt.Errorf(\"%s.MarshalJSON %s[%%d]: %%w\", idx, merr)\n",
			typeName, field.BaseName)
		builder.WriteString("\t\t\t}\n")
		builder.WriteString("\t\t\tmerged[key] = val\n")
		builder.WriteString("\t\t}\n")
		builder.WriteString("\t}\n\n")
	}

	builder.WriteString("\treturn json.Marshal(merged)\n")
	builder.WriteString("}\n\n")
}

// isIndexedParam reports whether name follows the PVE "base[n]" or
// "base[%d]" convention and returns (baseName, true) when it does.
// E.g. "net[n]" → ("net", true), "hostname" → ("", false).
func isIndexedParam(name string) (string, bool) {
	match := indexedParamRe.FindStringSubmatch(name)
	if match == nil {
		return "", false
	}

	return match[1], true
}

// sanitizeFieldName guarantees the result is a valid exported Go
// identifier. Some spec parameter names start with a digit or contain
// only digits; prefix those with "Field".
func sanitizeFieldName(fieldName string) string {
	if fieldName == "" {
		return "Field"
	}

	if firstChar := fieldName[0]; firstChar >= '0' && firstChar <= '9' {
		return "Field" + fieldName
	}

	return fieldName
}

// isAlreadyNilable reports whether the given Go type is naturally
// nil-able (slice, map, pointer, interface, json.RawMessage). Those
// do not need an extra * for optional encoding.
func isAlreadyNilable(goType string) bool {
	switch {
	case strings.HasPrefix(goType, "[]"):
		return true
	case strings.HasPrefix(goType, "map["):
		return true
	case strings.HasPrefix(goType, "*"):
		return true
	case goType == goTypeRawMessage:
		return true
	case goType == "interface{}":
		return true
	}

	return false
}

// renderResponseType emits "type <Method>Response ..." sized to the
// shape of the returns schema. Objects become typed structs; arrays
// of objects become []<inner-struct>; everything else falls back to a
// type alias over json.RawMessage so callers can decode further.
func renderResponseType(builder *strings.Builder, endpt endpoint) {
	name := responseTypeName(endpt)
	ret := endpt.Info.Returns

	switch {
	case ret == nil:
		return
	case ret.Type == schemaTypeObject && len(ret.Properties) > 0:
		body, err := renderObjectFields(ret)
		if err != nil {
			// Fallback to json.RawMessage alias on shapes we cannot
			// model. Do NOT call builder.Reset() here — that would wipe every
			// preceding endpoint in the same namespace.
			fmt.Fprintf(builder, "// %s is the raw JSON returned by %s %s. The spec\n", name, endpt.Verb, endpt.Path)
			fmt.Fprintf(builder, "// shape was not modellable as a Go struct; decode further as needed.\n")
			fmt.Fprintf(builder, "type %s = json.RawMessage\n\n", name)

			return
		}

		fmt.Fprintf(builder, "// %s mirrors the shape returned by %s %s.\n", name, endpt.Verb, endpt.Path)
		fmt.Fprintf(builder, "type %s struct {\n", name)
		builder.WriteString(body)
		builder.WriteString("}\n\n")
	case ret.Type == schemaTypeArray:
		inner := goTypeRawMessage

		if ret.Items != nil {
			it, err := goTypeFor(ret.Items)
			if err == nil {
				inner = responseFloatType(responseBoolType(it))
			}
		}

		fmt.Fprintf(builder, "// %s mirrors the shape returned by %s %s.\n", name, endpt.Verb, endpt.Path)
		fmt.Fprintf(builder, "type %s []%s\n\n", name, inner)
	default:
		fmt.Fprintf(builder, "// %s is the raw JSON returned by %s %s.\n", name, endpt.Verb, endpt.Path)
		fmt.Fprintf(builder, "type %s = json.RawMessage\n\n", name)
	}
}

// renderObjectFields returns the inner-body text of a typed struct
// (one field per top-level property of the object schema). Lines are
// already indented with a leading tab; the caller wraps "type X
// struct { ... }".
func renderObjectFields(objSchema *schema) (string, error) {
	if objSchema == nil || objSchema.Type != schemaTypeObject || len(objSchema.Properties) == 0 {
		return "", errMissingObjProps
	}

	names := make([]string, 0, len(objSchema.Properties))
	for name := range objSchema.Properties {
		names = append(names, name)
	}

	sort.Strings(names)

	var builder strings.Builder

	for _, name := range names {
		prop := objSchema.Properties[name]

		goType, err := goTypeFor(prop)
		if err != nil {
			// For response shapes, unknown types fall back to RawMessage.
			goType = goTypeRawMessage
		}

		// Response booleans and floats use the tolerant client.PVEBool and
		// client.PVEFloat: the PVE API renders booleans (true/false, 1/0,
		// "1"/"0", ...) and documented numbers (PSI pressure metrics arrive as
		// strings) inconsistently, so plain bool/float64 fail to decode real
		// get-by-id payloads. Request params keep plain types (see
		// emitRegularFields); only response shapes are retyped.
		goType = responseFloatType(responseBoolType(goType))

		opt := isOptional(prop)
		if opt && !isAlreadyNilable(goType) {
			goType = "*" + goType
		}

		fieldName := sanitizeFieldName(pascalize(name))

		desc := strings.TrimSpace(prop.Description)
		if desc != "" {
			fmt.Fprintf(&builder, "\t// %s %s\n", fieldName, escapeDoc(desc))
		}

		jsonTag := name
		if opt {
			jsonTag += ",omitempty"
		}

		fmt.Fprintf(&builder, "\t%s %s `json:\"%s\"`\n", fieldName, goType, jsonTag)
	}

	return builder.String(), nil
}

// renderMethodBodyHead emits the method signature, nil-ctx guard, and path
// expression into builder, returning the path expression string.
func renderMethodBodyHead(builder *strings.Builder, pkgName string, endpt endpoint, respGo string) {
	sig := renderMethodSignature(endpt, false)

	fmt.Fprintf(builder, "// %s implements Service.%s. %s %s.\n", endpt.GoMethod, endpt.GoMethod, endpt.Verb, endpt.Path)
	fmt.Fprintf(builder, "func %s {\n", sig)
	fmt.Fprintf(builder, "\tif ctx == nil {\n\t\treturn %s fmt.Errorf(\"%s.%s: ctx must not be nil\")\n\t}\n",
		zeroReturnExpr(respGo), pkgName, endpt.GoMethod)

	// Build the path with %s substitution for each path parameter.
	pathExpr := buildPathExpression(endpt)
	builder.WriteString("\tpath := " + pathExpr + "\n")
}

// renderMethodBodyParams emits the body map construction (marshal/unmarshal
// params) into builder.
func renderMethodBodyParams(builder *strings.Builder, pkgName string, endpt endpoint, respGo string) {
	hasParams := hasNonPathParams(endpt)
	if hasParams {
		builder.WriteString("\tvar body map[string]interface{}\n")
		builder.WriteString("\tif params != nil {\n")
		builder.WriteString("\t\traw, err := json.Marshal(params)\n")
		builder.WriteString("\t\tif err != nil {\n")
		fmt.Fprintf(builder, "\t\t\treturn %s fmt.Errorf(\"%s.%s: marshal params: %%w\", err)\n",
			zeroReturnExpr(respGo), pkgName, endpt.GoMethod)
		builder.WriteString("\t\t}\n")
		// Decode with UseNumber so integer params keep their exact digits.
		// Plain Unmarshal turns every JSON number into float64, which the
		// form encoder renders in scientific notation for values >= 1e6
		// (e.g. bwlimit=1048576 -> "1.048576e+06"), which PVE rejects.
		builder.WriteString("\t\tdec := json.NewDecoder(strings.NewReader(string(raw)))\n")
		builder.WriteString("\t\tdec.UseNumber()\n")
		builder.WriteString("\t\terr = dec.Decode(&body)\n")
		builder.WriteString("\t\tif err != nil {\n")
		fmt.Fprintf(builder, "\t\t\treturn %s fmt.Errorf(\"%s.%s: decode params: %%w\", err)\n",
			zeroReturnExpr(respGo), pkgName, endpt.GoMethod)
		builder.WriteString("\t\t}\n")
		builder.WriteString("\t}\n")
	} else {
		builder.WriteString("\tvar body map[string]interface{}\n")
	}
}

// renderMethodBodyDispatch emits the client dispatch call and nil-response
// guard into builder. Returns an error for unsupported verbs.
func renderMethodBodyDispatch(builder *strings.Builder, pkgName string, endpt endpoint, respGo string) error {
	var verbCall string

	switch strings.ToUpper(endpt.Verb) {
	case verbHTTPGet:
		verbCall = "GetRawCtx"
	case verbHTTPPost:
		verbCall = "PostRawCtx"
	case verbHTTPPut:
		verbCall = "PutRawCtx"
	case verbHTTPDelete:
		verbCall = "DeleteRawCtx"
	default:
		return fmt.Errorf("unsupported HTTP verb %q on %s: %w", endpt.Verb, endpt.Path, errNilSchema)
	}

	fmt.Fprintf(builder, "\tresp, err := s.c.%s(ctx, path, body)\n", verbCall)
	builder.WriteString("\tif err != nil {\n")
	fmt.Fprintf(builder, "\t\treturn %s fmt.Errorf(\"%s.%s: %%w\", err)\n", zeroReturnExpr(respGo), pkgName, endpt.GoMethod)
	builder.WriteString("\t}\n")
	builder.WriteString("\tif resp == nil {\n")
	fmt.Fprintf(builder, "\t\treturn %s fmt.Errorf(\"%s.%s: nil response from client\")\n",
		zeroReturnExpr(respGo), pkgName, endpt.GoMethod)
	builder.WriteString("\t}\n")

	return nil
}

// renderMethodBodyResponse emits the response-decoding block into builder.
func renderMethodBodyResponse(builder *strings.Builder, pkgName string, endpt endpoint) {
	respName := responseTypeName(endpt)

	builder.WriteString("\tif resp.Data == nil {\n")

	// For alias types and slice types, the zero value is the correct empty result.
	if isResponseEmptyOk(endpt) {
		fmt.Fprintf(builder, "\t\tout := %s{}\n", respName)
		builder.WriteString("\t\treturn &out, nil\n")
	} else {
		fmt.Fprintf(builder, "\t\treturn nil, fmt.Errorf(\"%s.%s: empty data in response (code=%%d)\", resp.Code)\n",
			pkgName, endpt.GoMethod)
	}

	builder.WriteString("\t}\n")
	builder.WriteString("\traw, err := json.Marshal(resp.Data)\n")
	builder.WriteString("\tif err != nil {\n")
	fmt.Fprintf(builder, "\t\treturn nil, fmt.Errorf(\"%s.%s: re-marshal data: %%w\", err)\n", pkgName, endpt.GoMethod)
	builder.WriteString("\t}\n")
	fmt.Fprintf(builder, "\tout := &%s{}\n", respName)
	builder.WriteString("\terr = json.Unmarshal(raw, out)\n")
	builder.WriteString("\tif err != nil {\n")
	fmt.Fprintf(builder, "\t\treturn nil, fmt.Errorf(\"%s.%s: unmarshal data: %%w\", err)\n", pkgName, endpt.GoMethod)
	builder.WriteString("\t}\n")
	builder.WriteString("\treturn out, nil\n")
	builder.WriteString("}\n\n")
}

// renderMethodBody emits the receiver-method implementation.
func renderMethodBody(builder *strings.Builder, pkgName string, endpt endpoint) error {
	respGo := responseGoType(endpt)

	renderMethodBodyHead(builder, pkgName, endpt, respGo)
	renderMethodBodyParams(builder, pkgName, endpt, respGo)

	err := renderMethodBodyDispatch(builder, pkgName, endpt, respGo)
	if err != nil {
		return err
	}

	if respGo == "" {
		builder.WriteString("\t_ = resp\n")
		builder.WriteString("\treturn nil\n")
		builder.WriteString("}\n\n")

		return nil
	}

	// Decode resp.Data into typed response via JSON round-trip.
	renderMethodBodyResponse(builder, pkgName, endpt)

	return nil
}

// isResponseEmptyOk reports whether an absent "data" field in the
// response should be treated as the empty zero value rather than an
// error. Array and aliased-RawMessage responses are tolerant; typed
// struct responses are strict.
func isResponseEmptyOk(endpt endpoint) bool {
	ret := endpt.Info.Returns
	if ret == nil {
		return true
	}

	if ret.Type == schemaTypeArray {
		return true
	}
	// Aliased RawMessage = anything not a populated object/array.
	if ret.Type != schemaTypeObject || len(ret.Properties) == 0 {
		return true
	}

	return false
}

// zeroReturnExpr returns the leading expression for an error-only or
// "(nil, ...)" return statement. For methods that return only an
// error it returns ""; for methods that return a (T, error) it
// returns "nil,".
func zeroReturnExpr(respGo string) string {
	if respGo == "" {
		return ""
	}

	return "nil,"
}

// buildPathExpression returns a Go expression that evaluates to the
// fully-substituted endpoint path at runtime. Each path parameter is
// escaped via url.PathEscape before substitution to prevent path
// traversal (e.g. a caller passing vmid="100/../../etc" would otherwise
// corrupt the constructed URL).
func buildPathExpression(endpt endpoint) string {
	if len(endpt.PathParams) == 0 {
		return strconvQuote(endpt.Path)
	}

	// Replace each {name} with %s in the format string.
	fmtPath := endpt.Path

	args := make([]string, 0, len(endpt.PathParams))

	for _, pathParam := range endpt.PathParams {
		fmtPath = strings.Replace(fmtPath, "{"+pathParam+"}", "%s", 1)
		args = append(args, "url.PathEscape("+goIdentSafe(camelize(pathParam))+")")
	}

	return fmt.Sprintf("fmt.Sprintf(%s, %s)", strconvQuote(fmtPath), strings.Join(args, ", "))
}

// strconvQuote is a tiny helper that returns a Go-quoted string
// literal for str. We use this instead of strconv.Quote to avoid
// pulling in the strconv import in the generator output (we only
// quote ASCII-safe paths from the spec).
func strconvQuote(str string) string {
	return "\"" + strings.ReplaceAll(str, "\"", "\\\"") + "\""
}

// goTypeFor maps a JSON-schema type to a Go type. Unknown shapes fall
// back to json.RawMessage so the generator stays total.
func goTypeFor(objSchema *schema) (string, error) {
	if objSchema == nil {
		return "", errNilSchema
	}

	switch objSchema.Type {
	case schemaTypeString:
		return goTypeString, nil
	case schemaTypeInteger:
		return goTypeInt64, nil
	case schemaTypeNumber:
		return goTypeFloat64, nil
	case schemaTypeBoolean:
		return goTypeBool, nil
	case schemaTypeArray:
		if objSchema.Items == nil {
			return "[]" + goTypeRawMessage, nil
		}

		inner, err := goTypeFor(objSchema.Items)
		if err != nil {
			return "[]" + goTypeRawMessage, nil //nolint:nilerr // intentional fallback
		}

		return "[]" + inner, nil
	case schemaTypeObject:
		// Nested objects fall back to raw JSON; emitting nested typed
		// structs recursively would explode the surface area beyond the
		// SW3 scope. Callers can decode further via json.Unmarshal.
		return goTypeRawMessage, nil
	case schemaTypeNull:
		return goTypeRawMessage, nil
	case "":
		return goTypeRawMessage, nil
	default:
		return goTypeRawMessage, nil
	}
}

// responseBoolType rewrites a bool Go type produced by goTypeFor into the
// tolerant client.PVEBool used in response shapes. Scalar "bool" and slice
// "[]bool" are handled; every other type is returned unchanged. Only response
// structs call this — request params keep plain bool so query encoding is
// unaffected.
func responseBoolType(goType string) string {
	switch goType {
	case goTypeBool:
		return "client.PVEBool"
	case "[]" + goTypeBool:
		return "[]client.PVEBool"
	default:
		return goType
	}
}

// responseFloatType rewrites a float Go type produced by goTypeFor into the
// tolerant client.PVEFloat used in response shapes. Scalar "float64" and slice
// "[]float64" are handled; every other type is returned unchanged. Only response
// structs call this — request params keep plain float64 so query encoding is
// unaffected. PVE renders documented numbers inconsistently (notably PSI
// pressure metrics arrive as strings), so a plain float64 fails to decode real
// payloads.
func responseFloatType(goType string) string {
	switch goType {
	case goTypeFloat64:
		return "client.PVEFloat"
	case "[]" + goTypeFloat64:
		return "[]client.PVEFloat"
	default:
		return goType
	}
}

// isOptional reports whether a schema's optional flag is truthy. The
// upstream spec encodes it as either the integer 1 or the string "1";
// any other shape (absent, 0, "0", null, "") means required.
func isOptional(objSchema *schema) bool {
	if objSchema == nil || len(objSchema.Optional) == 0 {
		return false
	}

	var asInt int

	err := json.Unmarshal(objSchema.Optional, &asInt)
	if err == nil {
		return asInt == 1
	}

	var asStr string

	err = json.Unmarshal(objSchema.Optional, &asStr)
	if err == nil {
		return asStr == "1" || strings.EqualFold(asStr, "true")
	}

	var asBool bool

	err = json.Unmarshal(objSchema.Optional, &asBool)
	if err == nil {
		return asBool
	}

	return false
}

// escapeDoc strips characters that break Go doc comments (mainly
// trailing whitespace and embedded newlines).
func escapeDoc(str string) string {
	str = strings.ReplaceAll(str, "\r\n", " ")
	str = strings.ReplaceAll(str, "\n", " ")
	str = strings.ReplaceAll(str, "\t", " ")

	return strings.TrimSpace(str)
}

// writeIfChanged writes data to path only when the existing content
// differs. This is a quality-of-life improvement for editor watchers
// and also keeps mtimes stable across no-op generator runs.
func writeIfChanged(path string, data []byte) error {
	existing, err := os.ReadFile(path)
	if err == nil && bytes.Equal(existing, data) {
		return nil
	}

	err = os.WriteFile(path, data, filePerm)
	if err != nil {
		return fmt.Errorf("write file: %w", err)
	}

	return nil
}
