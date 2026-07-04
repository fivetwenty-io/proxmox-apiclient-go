package main

import (
	"encoding/json"
	"testing"
)

// Shared test-fixture constants. Defined once and referenced by name (rather
// than repeated as raw string literals) across helpers_test.go and
// testgen_test.go so goconst has nothing to flag; the production-facing
// verb/type-name constants (verbHTTP*, schemaType*, goType*, methodPrefixGet,
// namespaceVersion) live in main.go/testgen.go and are reused here directly.
const (
	identNode                  = "node"
	identVmid                  = "vmid"
	identPascalNode            = "Node"
	pathVersion                = "/version"
	pathAccessUsers            = "/access/users"
	pathNodesQemuStatusCurrent = "/nodes/{node}/qemu/{vmid}/status/current"
	methodListUsers            = "ListUsers"
	fieldHostname              = "hostname"
	fieldForce                 = "force"
	fieldUserid                = "userid"
	indexedNetParam            = "net[n]"
	namespaceNodes             = "nodes"
	namespaceAccess            = "access"
)

func TestPascalize(t *testing.T) {
	t.Parallel()

	cases := []struct {
		in   string
		want string
	}{
		{"", ""},
		{identNode, identPascalNode},
		{"redirect-url", "RedirectUrl"},
		{"full_tokenid", "FullTokenid"},
		{"realm.type", "RealmType"},
		{indexedNetParam, "Netn"},
		{"ip[%d]", "Ipd"},
		{"a-b_c.d", "ABCD"},
	}

	for _, tc := range cases {
		if got := pascalize(tc.in); got != tc.want {
			t.Errorf("pascalize(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestCamelize(t *testing.T) {
	t.Parallel()

	cases := []struct {
		in   string
		want string
	}{
		{"", ""},
		{identNode, identNode},
		{identVmid, identVmid},
		{"policy-id", "policyId"},
	}

	for _, tc := range cases {
		if got := camelize(tc.in); got != tc.want {
			t.Errorf("camelize(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestGoIdentSafe(t *testing.T) {
	t.Parallel()

	if got := goIdentSafe("type"); got != "type_" {
		t.Errorf("goIdentSafe(%q) = %q, want %q", "type", got, "type_")
	}

	if got := goIdentSafe(identNode); got != identNode {
		t.Errorf("goIdentSafe(%q) = %q, want %q", identNode, got, identNode)
	}
}

func TestExtractPathParams(t *testing.T) {
	t.Parallel()

	cases := []struct {
		path string
		want []string
	}{
		{pathVersion, nil},
		{pathNodesQemuStatusCurrent, []string{identNode, identVmid}},
		{"/access/domains/{realm}", []string{"realm"}},
	}

	for _, tc := range cases {
		got := extractPathParams(tc.path)
		if !stringSlicesEqual(got, tc.want) {
			t.Errorf("extractPathParams(%q) = %v, want %v", tc.path, got, tc.want)
		}
	}
}

func TestEndsInPathParam(t *testing.T) {
	t.Parallel()

	cases := []struct {
		path string
		want bool
	}{
		{"/nodes/{node}", true},
		{"/nodes/{node}/qemu", false},
		{pathNodesQemuStatusCurrent, false},
		{"/", false},
	}

	for _, tc := range cases {
		if got := endsInPathParam(tc.path); got != tc.want {
			t.Errorf("endsInPathParam(%q) = %v, want %v", tc.path, got, tc.want)
		}
	}
}

func TestNamespaceOf(t *testing.T) {
	t.Parallel()

	cases := []struct {
		path string
		want string
	}{
		{pathVersion, namespaceVersion},
		{"/nodes/{node}/qemu/{vmid}", namespaceNodes},
		{"/", "root"},
	}

	for _, tc := range cases {
		if got := namespaceOf(tc.path); got != tc.want {
			t.Errorf("namespaceOf(%q) = %q, want %q", tc.path, got, tc.want)
		}
	}
}

func TestGoMethodBaseName(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name string
		ep   endpoint
		want string
	}{
		{
			name: "GET dynamic tail on an all-brace path falls back to GetRoot",
			ep:   endpoint{Path: "/{something}", Verb: verbHTTPGet, GoNamespace: "something"},
			want: "GetRoot",
		},
		{
			name: "GET dynamic tail whose resource equals the namespace keeps the full name",
			ep:   endpoint{Path: "/nodes/{node}", Verb: verbHTTPGet, GoNamespace: namespaceNodes},
			want: "GetNodes",
		},
		{
			name: "GET static tail -> List",
			ep:   endpoint{Path: pathAccessUsers, Verb: verbHTTPGet, GoNamespace: namespaceAccess},
			want: methodListUsers,
		},
		{
			name: "POST -> Create",
			ep:   endpoint{Path: pathAccessUsers, Verb: verbHTTPPost, GoNamespace: namespaceAccess},
			want: "CreateUsers",
		},
		{
			name: "PUT -> Update",
			ep:   endpoint{Path: "/access/users/{userid}", Verb: verbHTTPPut, GoNamespace: namespaceAccess},
			want: "UpdateUsers",
		},
		{
			name: "DELETE -> Delete",
			ep:   endpoint{Path: "/access/users/{userid}", Verb: verbHTTPDelete, GoNamespace: namespaceAccess},
			want: "DeleteUsers",
		},
		{
			name: "namespace prefix stripped",
			ep:   endpoint{Path: pathAccessUsers, Verb: verbHTTPGet, GoNamespace: namespaceAccess},
			want: methodListUsers,
		},
	}

	for _, tc := range cases {
		tc.ep.PathParams = extractPathParams(tc.ep.Path)

		if got := goMethodBaseName(tc.ep); got != tc.want {
			t.Errorf("%s: goMethodBaseName() = %q, want %q", tc.name, got, tc.want)
		}
	}
}

func TestAssignMethodNamesResolvesCollisions(t *testing.T) {
	t.Parallel()

	eps := []endpoint{
		{Path: "/pools", Verb: verbHTTPPut, GoNamespace: "pools"},
		{Path: "/pools/{poolid}", Verb: verbHTTPPut, GoNamespace: "pools"},
	}
	for idx := range eps {
		eps[idx].PathParams = extractPathParams(eps[idx].Path)
	}

	assignMethodNames(eps)

	if eps[0].GoMethod != "UpdatePools" {
		t.Errorf("eps[0].GoMethod = %q, want %q", eps[0].GoMethod, "UpdatePools")
	}

	if eps[1].GoMethod != "UpdatePools2" {
		t.Errorf("eps[1].GoMethod = %q, want %q", eps[1].GoMethod, "UpdatePools2")
	}
}

func TestAssignMethodNamesAppliesOverride(t *testing.T) {
	t.Parallel()

	eps := []endpoint{{Path: pathVersion, Verb: verbHTTPGet, GoNamespace: namespaceVersion}}
	assignMethodNames(eps)

	if eps[0].GoMethod != methodPrefixGet {
		t.Errorf("GoMethod = %q, want %q", eps[0].GoMethod, methodPrefixGet)
	}
}

func TestIsOptional(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name string
		raw  string
		want bool
	}{
		{"absent", ``, false},
		{"int 1", `1`, true},
		{"int 0", `0`, false},
		{"string 1", `"1"`, true},
		{"string 0", `"0"`, false},
		{"string true", `"true"`, true},
		{"string TRUE", `"TRUE"`, true},
		{schemaTypeNull, schemaTypeNull, false},
	}

	for _, tc := range cases {
		sch := &schema{}
		if tc.raw != "" {
			sch.Optional = json.RawMessage(tc.raw)
		}

		if got := isOptional(sch); got != tc.want {
			t.Errorf("%s: isOptional() = %v, want %v", tc.name, got, tc.want)
		}
	}

	if isOptional(nil) {
		t.Error("isOptional(nil) = true, want false")
	}
}

func TestIsIndexedParam(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name     string
		wantBase string
		wantOK   bool
	}{
		{indexedNetParam, "net", true},
		{"ip[%d]", "ip", true},
		{fieldHostname, "", false},
		{"net0", "", false},
		{"[n]", "", false},
	}

	for _, tc := range cases {
		base, ok := isIndexedParam(tc.name)
		if ok != tc.wantOK || base != tc.wantBase {
			t.Errorf("isIndexedParam(%q) = (%q, %v), want (%q, %v)", tc.name, base, ok, tc.wantBase, tc.wantOK)
		}
	}
}

func TestGoTypeFor(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name    string
		sch     *schema
		want    string
		wantErr bool
	}{
		{"nil schema", nil, "", true},
		{schemaTypeString, &schema{Type: schemaTypeString}, goTypeString, false},
		{schemaTypeInteger, &schema{Type: schemaTypeInteger}, goTypeInt64, false},
		{schemaTypeNumber, &schema{Type: schemaTypeNumber}, goTypeFloat64, false},
		{schemaTypeBoolean, &schema{Type: schemaTypeBoolean}, goTypeBool, false},
		{"array no items", &schema{Type: schemaTypeArray}, "[]" + goTypeRawMessage, false},
		{
			"array of string",
			&schema{Type: schemaTypeArray, Items: &schema{Type: schemaTypeString}},
			"[]" + goTypeString, false,
		},
		{
			"array of integer",
			&schema{Type: schemaTypeArray, Items: &schema{Type: schemaTypeInteger}},
			"[]" + goTypeInt64, false,
		},
		{"nested object", &schema{Type: schemaTypeObject}, goTypeRawMessage, false},
		{schemaTypeNull, &schema{Type: schemaTypeNull}, goTypeRawMessage, false},
		{"unknown", &schema{Type: "wat"}, goTypeRawMessage, false},
	}

	for _, tc := range cases {
		got, err := goTypeFor(tc.sch)
		if (err != nil) != tc.wantErr {
			t.Errorf("%s: goTypeFor() err = %v, wantErr %v", tc.name, err, tc.wantErr)
		}

		if got != tc.want {
			t.Errorf("%s: goTypeFor() = %q, want %q", tc.name, got, tc.want)
		}
	}
}

func TestIsAlreadyNilable(t *testing.T) {
	t.Parallel()

	cases := []struct {
		goType string
		want   bool
	}{
		{"[]" + goTypeString, true},
		{"map[int]string", true},
		{"*string", true},
		{goTypeRawMessage, true},
		{"interface{}", true},
		{goTypeString, false},
		{goTypeInt64, false},
		{goTypeBool, false},
	}

	for _, tc := range cases {
		if got := isAlreadyNilable(tc.goType); got != tc.want {
			t.Errorf("isAlreadyNilable(%q) = %v, want %v", tc.goType, got, tc.want)
		}
	}
}

func TestResponseBoolAndFloatType(t *testing.T) {
	t.Parallel()

	if got := responseBoolType(goTypeBool); got != "client.PVEBool" {
		t.Errorf("responseBoolType(bool) = %q, want client.PVEBool", got)
	}

	if got := responseBoolType("[]" + goTypeBool); got != "[]client.PVEBool" {
		t.Errorf("responseBoolType([]bool) = %q, want []client.PVEBool", got)
	}

	if got := responseBoolType(goTypeString); got != goTypeString {
		t.Errorf("responseBoolType(string) = %q, want string (unchanged)", got)
	}

	if got := responseFloatType(goTypeFloat64); got != "client.PVEFloat" {
		t.Errorf("responseFloatType(float64) = %q, want client.PVEFloat", got)
	}

	if got := responseFloatType("[]" + goTypeFloat64); got != "[]client.PVEFloat" {
		t.Errorf("responseFloatType([]float64) = %q, want []client.PVEFloat", got)
	}

	if got := responseFloatType(goTypeInt64); got != goTypeInt64 {
		t.Errorf("responseFloatType(int64) = %q, want int64 (unchanged)", got)
	}
}

func TestSanitizeFieldName(t *testing.T) {
	t.Parallel()

	cases := []struct {
		in   string
		want string
	}{
		{"", "Field"},
		{identPascalNode, identPascalNode},
		{"1Foo", "Field1Foo"},
	}

	for _, tc := range cases {
		if got := sanitizeFieldName(tc.in); got != tc.want {
			t.Errorf("sanitizeFieldName(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestHasNonPathParams(t *testing.T) {
	t.Parallel()

	epNoParams := endpoint{Info: endpointInfo{}}
	if hasNonPathParams(epNoParams) {
		t.Error("hasNonPathParams() = true for endpoint with no Parameters schema")
	}

	epOnlyPath := endpoint{
		PathParams: []string{identNode},
		Info: endpointInfo{
			Parameters: &schema{Properties: map[string]*schema{
				identNode: {Type: schemaTypeString},
			}},
		},
	}
	if hasNonPathParams(epOnlyPath) {
		t.Error("hasNonPathParams() = true when every property is a path parameter")
	}

	epWithBody := endpoint{
		PathParams: []string{identNode},
		Info: endpointInfo{
			Parameters: &schema{Properties: map[string]*schema{
				identNode:     {Type: schemaTypeString},
				fieldHostname: {Type: schemaTypeString},
			}},
		},
	}
	if !hasNonPathParams(epWithBody) {
		t.Error("hasNonPathParams() = false when a non-path property exists")
	}
}

func TestBuildPathExpression(t *testing.T) {
	t.Parallel()

	noParams := endpoint{Path: pathVersion}
	if got := buildPathExpression(noParams); got != `"/version"` {
		t.Errorf("buildPathExpression(no params) = %q, want %q", got, `"/version"`)
	}

	withParams := endpoint{Path: "/nodes/{node}/qemu/{vmid}", PathParams: []string{identNode, identVmid}}

	want := `fmt.Sprintf("/nodes/%s/qemu/%s", url.PathEscape(node), url.PathEscape(vmid))`
	if got := buildPathExpression(withParams); got != want {
		t.Errorf("buildPathExpression(with params) = %q, want %q", got, want)
	}
}

func TestBuildWantNS(t *testing.T) {
	t.Parallel()

	byNS := map[string][]endpoint{namespaceAccess: {{}}, namespaceNodes: {{}}}

	all := buildWantNS(nil, byNS)
	if len(all) != 2 || !all[namespaceAccess] || !all[namespaceNodes] {
		t.Errorf("buildWantNS(nil) = %v, want both namespaces selected", all)
	}

	subset := buildWantNS(stringSlice{namespaceAccess}, byNS)
	if len(subset) != 1 || !subset[namespaceAccess] {
		t.Errorf("buildWantNS([access]) = %v, want only access selected", subset)
	}

	unknown := buildWantNS(stringSlice{"bogus"}, byNS)
	if len(unknown) != 0 {
		t.Errorf("buildWantNS([bogus]) = %v, want empty (unknown namespace skipped)", unknown)
	}
}

func TestGroupByNamespace(t *testing.T) {
	t.Parallel()

	eps := []endpoint{
		{GoNamespace: namespaceAccess, Path: pathAccessUsers},
		{GoNamespace: namespaceNodes, Path: "/nodes"},
		{GoNamespace: namespaceAccess, Path: "/access/roles"},
	}

	got := groupByNamespace(eps)
	if len(got[namespaceAccess]) != 2 {
		t.Errorf("groupByNamespace() access bucket len = %d, want 2", len(got[namespaceAccess]))
	}

	if len(got[namespaceNodes]) != 1 {
		t.Errorf("groupByNamespace() nodes bucket len = %d, want 1", len(got[namespaceNodes]))
	}
}

func TestEscapeDoc(t *testing.T) {
	t.Parallel()

	in := "line one\nline\ttwo\r\nend  "
	want := "line one line two end"

	if got := escapeDoc(in); got != want {
		t.Errorf("escapeDoc(%q) = %q, want %q", in, got, want)
	}
}

func stringSlicesEqual(left, right []string) bool {
	if len(left) != len(right) {
		return false
	}

	for i := range left {
		if left[i] != right[i] {
			return false
		}
	}

	return true
}
