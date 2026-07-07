// Behavioral test generation for cmd/pvegen.
//
// This file renders the <namespace>_smoke_test.go sibling emitted next to
// every generated <namespace>_gen.go (see emitNamespace in main.go). The
// emitted test exercises EVERY generated endpoint — not just parameterless
// GETs — over a single shared httptest.Server per package:
//
//   - one subtest per endpoint calls the method with sample path arguments
//     and, when the endpoint has required (non-optional) body/query
//     parameters, a Params value with those fields populated with
//     deterministic sample data;
//   - the subtest asserts the recorded HTTP method and resolved URL path
//     match the spec exactly, that required parameter values reached the
//     wire (query string for GET/DELETE, form body for POST/PUT), and that
//     a canned JSON response decodes without error for typed responses;
//   - one additional subtest per distinct HTTP verb present in the package
//     asserts a 500 response surfaces as a non-nil error.
//
// Generation is deterministic: sample values are derived purely from the
// endpoint's spec-declared shape, and endpoints are iterated in the same
// sorted order used for source emission.
package main

import (
	"fmt"
	"sort"
	"strings"
)

// pathParamSampleValue returns a deterministic, URL-safe sample value for a
// path parameter. The value uses only unreserved URL characters (letters,
// digits, hyphens) so url.PathEscape at call time and the plain string
// substitution used to compute the expected request path agree exactly.
func pathParamSampleValue(name string) string {
	return "sample-" + strings.ToLower(name)
}

// sampleFloatLiteral is both the Go literal and the wire-encoded value a
// sample float64 field produces: json.Marshal(2.5) emits "2.5", and
// UseNumber-decoding + internal/http's json.Number encoding path preserves
// that exact text.
const sampleFloatLiteral = "2.5"

// sampleBoolLiteral is the Go literal for a sample bool field's value.
const sampleBoolLiteral = "true"

// zeroLiteralFor returns a Go literal that compiles for goType but carries no
// particular meaning. Used as a defensive fallback when sampleForGoType
// cannot derive a meaningful sample (no such shape exists in the spec today,
// but the generator must stay total).
// nilLiteral is the Go source text for a nil value, used as the compiling
// fallback literal for slice/map/unmodelled required-parameter shapes.
const nilLiteral = "nil"

func zeroLiteralFor(goType string) string {
	switch {
	case goType == goTypeString:
		return `""`
	case goType == goTypeInt64:
		return "0"
	case goType == goTypeFloat64:
		return "0"
	case goType == goTypeBool:
		return "false"
	case goType == goTypeRawMessage:
		return goTypeRawMessage + `("{}")`
	case strings.HasPrefix(goType, "[]"):
		return nilLiteral
	case strings.HasPrefix(goType, "map["):
		return nilLiteral
	default:
		return nilLiteral
	}
}

// sampleForGoType returns a Go literal for a value of goType plus the wire
// (query/form) string that value produces after round-tripping through the
// generated method's marshal→UseNumber-decode→internal/http encodeParams
// pipeline. The bool result is false when the wire encoding is not a simple,
// predictable string (e.g. json.RawMessage / nested slices of it) — the
// caller should assert only that the parameter key is present, not its
// value.
func sampleForGoType(goType, seed string) (string, string, bool) {
	switch goType {
	case goTypeString:
		wire := "sample-" + seed

		return strconvQuote(wire), wire, true
	case goTypeInt64:
		return "42", "42", true
	case goTypeFloat64:
		return sampleFloatLiteral, sampleFloatLiteral, true
	case goTypeBool:
		return sampleBoolLiteral, "1", true
	case goTypeRawMessage:
		// json.RawMessage marshals verbatim; "{}" decodes back into an
		// empty map on the request path, which internal/http encodes as
		// an empty string under the parameter key. The key is present on
		// the wire but its value is not meaningfully comparable here.
		return goTypeRawMessage + `("{}")`, "", false
	}

	if strings.HasPrefix(goType, "[]") {
		inner := strings.TrimPrefix(goType, "[]")

		innerLiteral, innerWire, innerComparable := sampleForGoType(inner, seed)
		if innerLiteral == "" {
			innerLiteral = zeroLiteralFor(inner)
		}

		return goType + "{" + innerLiteral + "}", innerWire, innerComparable
	}

	// No unmodelled required-parameter shape exists in the current spec
	// (goTypeFor only ever returns the cases above or a slice thereof).
	// Fall back to a compiling zero value rather than failing generation.
	return zeroLiteralFor(goType), "", false
}

// behaviorParam carries the data needed to populate one required field in a
// generated Params literal and to assert its presence/value on the wire.
type behaviorParam struct {
	// Name is the spec/json wire key (e.g. "redirect-url").
	Name string
	// FieldName is the exported Go struct field name (e.g. "RedirectUrl").
	FieldName string
	// Literal is the Go source expression assigned to FieldName.
	Literal string
	// WireValue is the expected query/form string value, valid only when
	// Comparable is true.
	WireValue string
	// Comparable reports whether WireValue should be asserted exactly, as
	// opposed to only asserting the key is present.
	Comparable bool
}

// collectBehaviorParams returns one behaviorParam per required (non-optional,
// non-path, non-indexed) top-level property of endpt's parameter schema,
// sorted by wire name for determinism. Required indexed ("base[n]") params do
// not occur anywhere in the current spec (verified against _data/apidoc.json);
// if one were introduced, it is skipped here rather than emitting unbuildable
// map[int]string construction code — a future generator enhancement, not a
// silent correctness gap, since collectBehaviorParams only feeds sample-value
// assertions, never the Params struct's actual field set.
func collectBehaviorParams(endpt endpoint) []behaviorParam {
	if endpt.Info.Parameters == nil {
		return nil
	}

	pathSet := map[string]bool{}
	for _, pathParam := range endpt.PathParams {
		pathSet[pathParam] = true
	}

	names := make([]string, 0, len(endpt.Info.Parameters.Properties))
	for name := range endpt.Info.Parameters.Properties {
		names = append(names, name)
	}

	sort.Strings(names)

	var out []behaviorParam

	for _, name := range names {
		if pathSet[name] {
			continue
		}

		prop := endpt.Info.Parameters.Properties[name]
		if isOptional(prop) {
			continue
		}

		if _, ok := isIndexedParam(name); ok {
			continue
		}

		goType, err := goTypeFor(prop)
		if err != nil {
			goType = goTypeRawMessage
		}

		fieldName := sanitizeFieldName(pascalize(name))
		literal, wire, isComparable := sampleForGoType(goType, name)

		out = append(out, behaviorParam{
			Name:       name,
			FieldName:  fieldName,
			Literal:    literal,
			WireValue:  wire,
			Comparable: isComparable,
		})
	}

	return out
}

// buildParamsArgExpr returns the Go source expression for the trailing
// *<Method>Params argument of a generated call, or "" when the endpoint
// takes no such argument. When the endpoint has non-path parameters but none
// are required, an empty struct literal is emitted (rather than nil) so the
// generated test still exercises the params-marshal code path.
func buildParamsArgExpr(pkgName string, endpt endpoint, params []behaviorParam) string {
	if !hasNonPathParams(endpt) {
		return ""
	}

	if len(params) == 0 {
		return fmt.Sprintf("&%s.%sParams{}", pkgName, endpt.GoMethod)
	}

	fields := make([]string, 0, len(params))
	for _, param := range params {
		fields = append(fields, fmt.Sprintf("%s: %s", param.FieldName, param.Literal))
	}

	return fmt.Sprintf("&%s.%sParams{%s}", pkgName, endpt.GoMethod, strings.Join(fields, ", "))
}

// buildCallExprArgs returns the ordered argument expressions for a generated
// method call: ctxExpr (normally "ctx", or "nil" for the nil-context guard
// subtest), one quoted sample string per path parameter, and (when
// applicable) the Params argument built by buildParamsArgExpr.
func buildCallExprArgs(ctxExpr, pkgName string, endpt endpoint, params []behaviorParam) []string {
	args := []string{ctxExpr}

	for _, pathParam := range endpt.PathParams {
		args = append(args, strconvQuote(pathParamSampleValue(pathParam)))
	}

	if paramsExpr := buildParamsArgExpr(pkgName, endpt, params); paramsExpr != "" {
		args = append(args, paramsExpr)
	}

	return args
}

// apiBasePath is the fixed path prefix pkg/client.Options.GetBaseURL bakes
// into every request's base URL, ahead of the endpoint path itself.
const apiBasePath = "/api2/json"

// expectedRequestPath returns the fully-substituted request path a subtest
// should observe, using the same sample values buildCallExprArgs passes to
// the method call, prefixed with the client's fixed /api2/json base path.
func expectedRequestPath(endpt endpoint) string {
	path := endpt.Path
	for _, pathParam := range endpt.PathParams {
		path = strings.Replace(path, "{"+pathParam+"}", pathParamSampleValue(pathParam), 1)
	}

	return apiBasePath + path
}

// paramFieldQuery and paramFieldForm are the recordedRequest field names
// paramValuesField selects between.
const (
	paramFieldQuery = "query"
	paramFieldForm  = "form"
)

// paramValuesField returns the recordedRequest field name (paramFieldQuery or
// paramFieldForm) holding the wire-encoded parameters for the given HTTP
// verb, mirroring internal/http buildRequestWithContext: GET/DELETE
// parameters go in the query string, POST/PUT parameters go in the form
// body.
func paramValuesField(verb string) string {
	switch strings.ToUpper(verb) {
	case verbHTTPPost, verbHTTPPut:
		return paramFieldForm
	default:
		return paramFieldQuery
	}
}

// cannedResponseBody returns the JSON envelope body the mock server should
// return for a successful call to endpt. Array-shaped responses require a
// JSON array under "data" (a JSON object fails to unmarshal into a Go slice
// type); every other shape (object, scalar/RawMessage alias, or no
// response) accepts a JSON object.
// cannedArrayBody and cannedObjectBody are the two response envelope shapes
// cannedResponseBody selects between.
const (
	cannedArrayBody  = `{"data":[],"success":1}`
	cannedObjectBody = `{"data":{},"success":1}`
)

func cannedResponseBody(endpt endpoint) string {
	if endpt.Info.Returns != nil && endpt.Info.Returns.Type == schemaTypeArray {
		return cannedArrayBody
	}

	return cannedObjectBody
}

// renderNamespaceSmokeTest builds the full behavioral test source for a
// namespace: a shared-harness preamble plus one subtest per endpoint and a
// handful of per-verb error-path subtests. The file/function names retain
// the historical "smoke test" naming (see emitNamespaceSmokeTest in main.go)
// so the golangci-lint exclusion list keyed on "*_smoke_test.go" continues to
// apply without an unrelated lint-config edit.
func renderNamespaceSmokeTest(pkgName, nsName string, eps []endpoint) []byte {
	var builder strings.Builder

	renderBehaviorTestHeader(&builder, pkgName)
	renderBehaviorTestBody(&builder, pkgName, eps)

	_ = nsName

	return []byte(builder.String())
}

// renderBehaviorTestHeader writes the package declaration, imports, and the
// shared test harness (mock server, request recorder, assertion helpers)
// used by every subtest in the file.
func renderBehaviorTestHeader(builder *strings.Builder, pkgName string) {
	renderBehaviorTestPreamble(builder, pkgName)
	renderTestHarnessTypes(builder)
	renderTestHarnessMethods(builder)
	renderAssertionHelpers(builder)
}

// renderBehaviorTestPreamble writes the package declaration, imports, the
// anti-unused-import guard, and the smokeOptsFromServerURL helper used to
// build client options from the mock server's URL.
func renderBehaviorTestPreamble(builder *strings.Builder, pkgName string) {
	fmt.Fprintf(builder, "// Code generated by cmd/pvegen. DO NOT EDIT.\n\n")
	fmt.Fprintf(builder, "package %s_test\n\n", pkgName)

	builder.WriteString("import (\n")
	builder.WriteString("\t\"context\"\n")
	builder.WriteString("\t\"encoding/json\"\n")
	builder.WriteString("\t\"net/http\"\n")
	builder.WriteString("\t\"net/http/httptest\"\n")
	builder.WriteString("\t\"net/url\"\n")
	builder.WriteString("\t\"strconv\"\n")
	builder.WriteString("\t\"sync\"\n")
	builder.WriteString("\t\"testing\"\n\n")
	fmt.Fprintf(builder, "\t\"github.com/fivetwenty-io/proxmox-apiclient-go/v3/pkg/api/%s\"\n", pkgName)
	builder.WriteString("\tpveclient \"github.com/fivetwenty-io/proxmox-apiclient-go/v3/pkg/client\"\n")
	builder.WriteString(")\n\n")

	// Anti-unused-import guard: exercised unconditionally regardless of
	// whether this package's endpoints happen to require a json.RawMessage
	// sample value.
	builder.WriteString("var _ = json.RawMessage(nil)\n\n")

	builder.WriteString(`func smokeOptsFromServerURL(u string) pveclient.Options {
	parsed, err := url.Parse(u)
	if err != nil {
		panic("test setup: invalid server URL: " + err.Error())
	}

	host := parsed.Hostname()

	port := 0
	if p := parsed.Port(); p != "" {
		port, _ = strconv.Atoi(p)
	}

	return pveclient.Options{
		Host:     host,
		Port:     port,
		Protocol: "http",
		APIToken: "user@pam!tok=sec",
	}
}

`)
}

// renderTestHarnessTypes writes the recordedRequest and testHarness type
// declarations shared by every subtest in the file.
func renderTestHarnessTypes(builder *strings.Builder) {
	builder.WriteString(`// recordedRequest captures what the mock server observed for the most
// recently dispatched request.
type recordedRequest struct {
	method string
	path   string
	query  url.Values
	form   url.Values
}

// testHarness runs a single httptest.Server shared by every subtest in this
// file. Subtests are never run in parallel (each sets the desired response
// immediately before invoking the method under test, then reads back the
// recorded request), so the mutex below only guards against the harness
// goroutine and the test goroutine overlapping on a single in-flight
// request, never against concurrent subtests.
type testHarness struct {
	mu       sync.Mutex
	last     recordedRequest
	nextCode int
	nextBody string
}

`)
}

// renderTestHarnessMethods writes newTestHarness plus the testHarness
// methods used to configure responses and inspect recorded requests.
func renderTestHarnessMethods(builder *strings.Builder) {
	builder.WriteString(`// newTestHarness starts the mock server and returns it alongside the harness
// used to configure responses and inspect recorded requests.
func newTestHarness() (*httptest.Server, *testHarness) {
	h := &testHarness{nextCode: http.StatusOK}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h.mu.Lock()
		defer h.mu.Unlock()

		_ = r.ParseForm()
		h.last = recordedRequest{
			method: r.Method,
			path:   r.URL.Path,
			query:  r.URL.Query(),
			form:   r.PostForm,
		}

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(h.nextCode)
		_, _ = w.Write([]byte(h.nextBody))
	}))

	return srv, h
}

// set configures the status code and body the next request will receive.
func (h *testHarness) set(code int, body string) {
	h.mu.Lock()
	defer h.mu.Unlock()

	h.nextCode = code
	h.nextBody = body
}

// snapshot returns a copy of the most recently recorded request.
func (h *testHarness) snapshot() recordedRequest {
	h.mu.Lock()
	defer h.mu.Unlock()

	return h.last
}

`)
}

// renderAssertionHelpers writes the assertRequestLine, assertParamValue, and
// assertParamPresent helpers used by every generated subtest.
func renderAssertionHelpers(builder *strings.Builder) {
	builder.WriteString(`// assertRequestLine fails the test when the recorded method or resolved
// path does not exactly match what the endpoint's spec entry declares.
func assertRequestLine(t *testing.T, got recordedRequest, wantMethod, wantPath string) {
	t.Helper()

	if got.method != wantMethod {
		t.Errorf("method = %q, want %q", got.method, wantMethod)
	}

	if got.path != wantPath {
		t.Errorf("path = %q, want %q", got.path, wantPath)
	}
}

// assertParamValue fails the test when the wire-encoded value of key does
// not exactly equal want.
func assertParamValue(t *testing.T, values url.Values, key, want string) {
	t.Helper()

	if got := values.Get(key); got != want {
		t.Errorf("param %q = %q, want %q", key, got, want)
	}
}

// assertParamPresent fails the test when key is absent from values. Used for
// required parameters whose wire encoding is not a simple comparable string
// (e.g. json.RawMessage-typed fields).
func assertParamPresent(t *testing.T, values url.Values, key string) {
	t.Helper()

	if _, ok := values[key]; !ok {
		t.Errorf("param %q missing from request", key)
	}
}

`)
}

// renderBehaviorTestBody writes the single TestGenerated_<Pkg>_Methods
// function containing one subtest per endpoint plus the per-verb error-path
// subtests.
func renderBehaviorTestBody(builder *strings.Builder, pkgName string, eps []endpoint) {
	fmt.Fprintf(builder, "func TestGenerated_%s_Methods(t *testing.T) {\n", pascalize(pkgName))
	builder.WriteString("\tsrv, harness := newTestHarness()\n")
	builder.WriteString("\tdefer srv.Close()\n\n")
	builder.WriteString("\tc, err := pveclient.NewClient(smokeOptsFromServerURL(srv.URL))\n")
	builder.WriteString("\tif err != nil {\n")
	builder.WriteString("\t\tt.Fatalf(\"NewClient: %v\", err)\n")
	builder.WriteString("\t}\n\n")
	fmt.Fprintf(builder, "\tsvc := %s.New(c)\n", pkgName)
	builder.WriteString("\tctx := context.Background()\n\n")

	for _, endpt := range eps {
		renderEndpointSubtest(builder, pkgName, endpt)
	}

	renderErrorPathSubtests(builder, pkgName, eps)

	builder.WriteString("}\n")
}

// renderEndpointSubtest writes one t.Run subtest exercising a single
// endpoint: it configures a canned success response, calls the method with
// sample arguments, asserts the call succeeded (and, for typed responses,
// that the response pointer is non-nil), then asserts the recorded request's
// method, path, and required-parameter wire values.
func renderEndpointSubtest(builder *strings.Builder, pkgName string, endpt endpoint) {
	params := collectBehaviorParams(endpt)
	callArgs := buildCallExprArgs("ctx", pkgName, endpt, params)
	callExpr := fmt.Sprintf("svc.%s(%s)", endpt.GoMethod, strings.Join(callArgs, ", "))
	nilCtxArgs := buildCallExprArgs("nilCtx", pkgName, endpt, params)
	nilCtxCallExpr := fmt.Sprintf("svc.%s(%s)", endpt.GoMethod, strings.Join(nilCtxArgs, ", "))
	expectedPath := expectedRequestPath(endpt)
	verb := strings.ToUpper(endpt.Verb)
	valuesField := paramValuesField(endpt.Verb)
	respGo := responseGoType(endpt)

	fmt.Fprintf(builder, "\tt.Run(%q, func(t *testing.T) {\n", endpt.GoMethod)
	fmt.Fprintf(builder, "\t\tharness.set(http.StatusOK, `%s`)\n\n", cannedResponseBody(endpt))

	if respGo == "" {
		fmt.Fprintf(builder, "\t\terr := %s\n", callExpr)
		builder.WriteString("\t\tif err != nil {\n")
		fmt.Fprintf(builder, "\t\t\tt.Fatalf(\"%s: unexpected error: %%v\", err)\n", endpt.GoMethod)
		builder.WriteString("\t\t}\n\n")
	} else {
		fmt.Fprintf(builder, "\t\tresp, err := %s\n", callExpr)
		builder.WriteString("\t\tif err != nil {\n")
		fmt.Fprintf(builder, "\t\t\tt.Fatalf(\"%s: unexpected error: %%v\", err)\n", endpt.GoMethod)
		builder.WriteString("\t\t}\n")
		builder.WriteString("\t\tif resp == nil {\n")
		fmt.Fprintf(builder, "\t\t\tt.Fatal(\"%s: response is nil\")\n", endpt.GoMethod)
		builder.WriteString("\t\t}\n\n")
	}

	builder.WriteString("\t\tgot := harness.snapshot()\n")
	fmt.Fprintf(builder, "\t\tassertRequestLine(t, got, %q, %q)\n", verb, expectedPath)

	for _, param := range params {
		if param.Comparable {
			fmt.Fprintf(builder, "\t\tassertParamValue(t, got.%s, %q, %q)\n", valuesField, param.Name, param.WireValue)
		} else {
			fmt.Fprintf(builder, "\t\tassertParamPresent(t, got.%s, %q)\n", valuesField, param.Name)
		}
	}

	builder.WriteString("\n")

	// A typed nil context.Context (rather than a literal nil argument)
	// deliberately exercises the generated method's nil-ctx guard without
	// tripping staticcheck's SA1012 ("do not pass a nil Context"), which
	// only flags the literal nil identifier at a call site, not a nil-valued
	// variable.
	builder.WriteString("\t\tvar nilCtx context.Context\n")

	if respGo == "" {
		fmt.Fprintf(builder, "\t\tif err := %s; err == nil {\n", nilCtxCallExpr)
	} else {
		fmt.Fprintf(builder, "\t\tif _, err := %s; err == nil {\n", nilCtxCallExpr)
	}

	fmt.Fprintf(builder, "\t\t\tt.Errorf(\"%s: expected error for nil context, got nil\")\n", endpt.GoMethod)
	builder.WriteString("\t\t}\n")

	builder.WriteString("\t})\n")
}

// renderErrorPathSubtests writes one t.Run subtest per distinct HTTP verb
// present in eps, reusing the first endpoint seen for that verb: it
// configures a 500 response and asserts the method returns a non-nil error.
func renderErrorPathSubtests(builder *strings.Builder, pkgName string, eps []endpoint) {
	seenVerb := map[string]bool{}

	for _, endpt := range eps {
		verb := strings.ToUpper(endpt.Verb)
		if seenVerb[verb] {
			continue
		}

		seenVerb[verb] = true

		params := collectBehaviorParams(endpt)
		callArgs := buildCallExprArgs("ctx", pkgName, endpt, params)
		callExpr := fmt.Sprintf("svc.%s(%s)", endpt.GoMethod, strings.Join(callArgs, ", "))
		respGo := responseGoType(endpt)

		fmt.Fprintf(builder, "\tt.Run(%q, func(t *testing.T) {\n", "ErrorPath_"+verb)
		builder.WriteString("\t\tharness.set(http.StatusInternalServerError, " +
			"`{\"data\":null,\"success\":0,\"message\":\"boom\"}`)\n\n")

		if respGo == "" {
			fmt.Fprintf(builder, "\t\terr := %s\n", callExpr)
		} else {
			fmt.Fprintf(builder, "\t\t_, err := %s\n", callExpr)
		}

		builder.WriteString("\t\tif err == nil {\n")
		fmt.Fprintf(builder, "\t\t\tt.Fatalf(\"%s: expected error on 500 response, got nil\")\n", endpt.GoMethod)
		builder.WriteString("\t\t}\n")
		builder.WriteString("\t})\n")
	}
}
