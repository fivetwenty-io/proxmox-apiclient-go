package main

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestPathParamSampleValue(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name string
		want string
	}{
		{identNode, "sample-node"},
		{identVmid, "sample-vmid"},
		{"UPID", "sample-upid"},
	}

	for _, tc := range cases {
		if got := pathParamSampleValue(tc.name); got != tc.want {
			t.Errorf("pathParamSampleValue(%q) = %q, want %q", tc.name, got, tc.want)
		}
	}
}

func TestZeroLiteralFor(t *testing.T) {
	t.Parallel()

	cases := []struct {
		goType string
		want   string
	}{
		{goTypeString, `""`},
		{goTypeInt64, "0"},
		{goTypeFloat64, "0"},
		{goTypeBool, "false"},
		{goTypeRawMessage, goTypeRawMessage + `("{}")`},
		{"[]" + goTypeString, nilLiteral},
		{"map[int]string", nilLiteral},
		{"something.Unknown", nilLiteral},
	}

	for _, tc := range cases {
		if got := zeroLiteralFor(tc.goType); got != tc.want {
			t.Errorf("zeroLiteralFor(%q) = %q, want %q", tc.goType, got, tc.want)
		}
	}
}

func TestSampleForGoType(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name           string
		goType         string
		seed           string
		wantLiteral    string
		wantWire       string
		wantComparable bool
	}{
		{goTypeString, goTypeString, fieldHostname, `"sample-hostname"`, "sample-hostname", true},
		{goTypeInt64, goTypeInt64, "cores", "42", "42", true},
		{goTypeFloat64, goTypeFloat64, "bwlimit", sampleFloatLiteral, sampleFloatLiteral, true},
		{goTypeBool, goTypeBool, fieldForce, sampleBoolLiteral, "1", true},
		{
			"raw message", goTypeRawMessage, "info",
			goTypeRawMessage + `("{}")`, "", false,
		},
		{
			"slice of string", "[]" + goTypeString, "roles",
			`[]string{"sample-roles"}`, "sample-roles", true,
		},
		{
			"slice of int64", "[]" + goTypeInt64, "vmids",
			"[]int64{42}", "42", true,
		},
		{
			"slice of raw message", "[]" + goTypeRawMessage, "entries",
			`[]json.RawMessage{json.RawMessage("{}")}`, "", false,
		},
		{
			"unmodelled scalar falls back to a compiling zero value", "time.Time", "when",
			nilLiteral, "", false,
		},
	}

	for _, tc := range cases {
		literal, wire, isComparable := sampleForGoType(tc.goType, tc.seed)
		if literal != tc.wantLiteral {
			t.Errorf("%s: literal = %q, want %q", tc.name, literal, tc.wantLiteral)
		}

		if wire != tc.wantWire {
			t.Errorf("%s: wire = %q, want %q", tc.name, wire, tc.wantWire)
		}

		if isComparable != tc.wantComparable {
			t.Errorf("%s: comparable = %v, want %v", tc.name, isComparable, tc.wantComparable)
		}
	}
}

func TestCollectBehaviorParams(t *testing.T) {
	t.Parallel()

	endpt := endpoint{
		PathParams: []string{identNode},
		Info: endpointInfo{
			Parameters: &schema{
				Properties: map[string]*schema{
					identNode:       {Type: schemaTypeString},                                  // path param: excluded
					fieldHostname:   {Type: schemaTypeString},                                  // required
					"cpu":           {Type: schemaTypeInteger, Optional: json.RawMessage(`1`)}, // optional: excluded
					indexedNetParam: {Type: schemaTypeString},                                  // required indexed: excluded (no such shape in the spec today)
					fieldForce:      {Type: schemaTypeBoolean},                                 // required
				},
			},
		},
	}

	got := collectBehaviorParams(endpt)

	names := make([]string, 0, len(got))
	for _, p := range got {
		names = append(names, p.Name)
	}

	want := []string{fieldForce, fieldHostname} // sorted by wire name

	if !stringSlicesEqual(names, want) {
		t.Fatalf("collectBehaviorParams() names = %v, want %v", names, want)
	}

	for _, p := range got {
		switch p.Name {
		case fieldHostname:
			if p.FieldName != "Hostname" || p.Literal != `"sample-hostname"` || !p.Comparable {
				t.Errorf("hostname param = %+v, unexpected shape", p)
			}
		case fieldForce:
			if p.FieldName != "Force" || p.Literal != sampleBoolLiteral || p.WireValue != "1" || !p.Comparable {
				t.Errorf("force param = %+v, unexpected shape", p)
			}
		}
	}
}

func TestCollectBehaviorParamsNilParameters(t *testing.T) {
	t.Parallel()

	if got := collectBehaviorParams(endpoint{}); got != nil {
		t.Errorf("collectBehaviorParams(no Parameters) = %v, want nil", got)
	}
}

func TestBuildParamsArgExpr(t *testing.T) {
	t.Parallel()

	noParams := endpoint{GoMethod: methodListUsers}
	if got := buildParamsArgExpr(namespaceAccess, noParams, nil); got != "" {
		t.Errorf("buildParamsArgExpr(no params) = %q, want empty", got)
	}

	optionalOnly := endpoint{
		GoMethod: methodListUsers,
		Info: endpointInfo{
			Parameters: &schema{Properties: map[string]*schema{
				"enabled": {Type: schemaTypeBoolean, Optional: json.RawMessage(`1`)},
			}},
		},
	}

	want := "&access.ListUsersParams{}"
	if got := buildParamsArgExpr(namespaceAccess, optionalOnly, nil); got != want {
		t.Errorf("buildParamsArgExpr(optional-only) = %q, want %q", got, want)
	}

	withRequired := endpoint{
		GoMethod: "CreateUsers",
		Info: endpointInfo{
			Parameters: &schema{Properties: map[string]*schema{
				fieldUserid: {Type: schemaTypeString},
			}},
		},
	}
	params := []behaviorParam{{Name: fieldUserid, FieldName: "Userid", Literal: `"sample-userid"`}}

	wantWithRequired := `&access.CreateUsersParams{Userid: "sample-userid"}`
	if got := buildParamsArgExpr(namespaceAccess, withRequired, params); got != wantWithRequired {
		t.Errorf("buildParamsArgExpr(required) = %q, want %q", got, wantWithRequired)
	}
}

func TestBuildCallExprArgs(t *testing.T) {
	t.Parallel()

	endpt := endpoint{
		GoMethod:   "UpdateUsers",
		PathParams: []string{fieldUserid},
		Info: endpointInfo{
			Parameters: &schema{Properties: map[string]*schema{
				fieldUserid: {Type: schemaTypeString},
				"comment":   {Type: schemaTypeString},
			}},
		},
	}

	params := collectBehaviorParams(endpt)
	got := buildCallExprArgs("ctx", namespaceAccess, endpt, params)

	want := []string{"ctx", `"sample-userid"`, `&access.UpdateUsersParams{Comment: "sample-comment"}`}
	if !stringSlicesEqual(got, want) {
		t.Errorf("buildCallExprArgs() = %v, want %v", got, want)
	}

	nilCtx := buildCallExprArgs(nilLiteral, namespaceAccess, endpt, params)
	if len(nilCtx) == 0 || nilCtx[0] != nilLiteral {
		t.Errorf("buildCallExprArgs(nil ctx)[0] = %v, want %q as the first argument", nilCtx, nilLiteral)
	}
}

func TestExpectedRequestPath(t *testing.T) {
	t.Parallel()

	endpt := endpoint{Path: pathNodesQemuStatusCurrent, PathParams: []string{identNode, identVmid}}

	want := "/api2/json/nodes/sample-node/qemu/sample-vmid/status/current"
	if got := expectedRequestPath(endpt); got != want {
		t.Errorf("expectedRequestPath() = %q, want %q", got, want)
	}
}

func TestParamValuesField(t *testing.T) {
	t.Parallel()

	cases := []struct {
		verb string
		want string
	}{
		{verbHTTPGet, paramFieldQuery},
		{"get", paramFieldQuery},
		{verbHTTPDelete, paramFieldQuery},
		{verbHTTPPost, paramFieldForm},
		{verbHTTPPut, paramFieldForm},
	}

	for _, tc := range cases {
		if got := paramValuesField(tc.verb); got != tc.want {
			t.Errorf("paramValuesField(%q) = %q, want %q", tc.verb, got, tc.want)
		}
	}
}

func TestCannedResponseBody(t *testing.T) {
	t.Parallel()

	arrayResp := endpoint{Info: endpointInfo{Returns: &schema{Type: schemaTypeArray}}}
	if got := cannedResponseBody(arrayResp); got != cannedArrayBody {
		t.Errorf("cannedResponseBody(array) = %q", got)
	}

	objectResp := endpoint{Info: endpointInfo{Returns: &schema{Type: schemaTypeObject, Properties: map[string]*schema{
		"upid": {Type: schemaTypeString},
	}}}}
	if got := cannedResponseBody(objectResp); got != cannedObjectBody {
		t.Errorf("cannedResponseBody(object) = %q", got)
	}

	noResp := endpoint{}
	if got := cannedResponseBody(noResp); got != cannedObjectBody {
		t.Errorf("cannedResponseBody(no returns) = %q", got)
	}
}

// TestRenderNamespaceSmokeTestCompiles is a light smoke check that the
// full render pipeline produces syntactically valid, non-empty Go source
// containing the expected harness and subtest scaffolding, without
// depending on go/format (format.Source is exercised end-to-end by
// emitNamespaceSmokeTest, covered indirectly via verify-generated).
func TestRenderNamespaceSmokeTestCompiles(t *testing.T) {
	t.Parallel()

	eps := []endpoint{
		{
			Path: "/widgets", Verb: verbHTTPGet, GoMethod: "ListWidgets", GoNamespace: "widgets",
			Info: endpointInfo{Returns: &schema{Type: schemaTypeArray, Items: &schema{Type: schemaTypeString}}},
		},
		{
			Path: "/widgets", Verb: verbHTTPPost, GoMethod: "CreateWidgets", GoNamespace: "widgets",
			Info: endpointInfo{
				Parameters: &schema{Properties: map[string]*schema{"name": {Type: schemaTypeString}}},
			},
		},
	}

	src := renderNamespaceSmokeTest("widgets", "widgets", eps)
	text := string(src)

	for _, want := range []string{
		"package widgets_test",
		"func TestGenerated_Widgets_Methods(t *testing.T)",
		`t.Run("ListWidgets"`,
		`t.Run("CreateWidgets"`,
		`t.Run("ErrorPath_GET"`,
		`t.Run("ErrorPath_POST"`,
		"assertRequestLine(t, got,",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("rendered source missing %q", want)
		}
	}
}
