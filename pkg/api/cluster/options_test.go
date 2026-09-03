package cluster_test

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"

	"github.com/fivetwenty-io/proxmox-apiclient-go/v3/pkg/api/cluster"
	pveclient "github.com/fivetwenty-io/proxmox-apiclient-go/v3/pkg/client"
)

// clusterOptionsBody is a GET /cluster/options payload in the shape
// PVE::DataCenterConfig::parse_datacenter_config produces (pve-cluster
// 9.x): every property-string option arrives already split into an
// object keyed by sub-option, and registered-tags arrives as an array.
// The spec declares all of them as plain strings; cmd/pvegen corrects the
// eleven affected properties through returnsPropertyOverrides. The values
// are hand-authored from the parser's rules, not captured from a live
// cluster, so they pin the container shapes rather than any exact
// sub-option set.
const clusterOptionsBody = `{"data":{
	"keyboard":"en-us",
	"max_workers":4,
	"migration_unsecure":0,
	"crs":{"ha":"static","ha-rebalance-on-start":1},
	"ha":{"shutdown_policy":"migrate"},
	"migration":{"type":"secure","network":"10.10.0.0/24"},
	"next-id":{"lower":100,"upper":1000000},
	"notify":{"fencing":"always","target-fencing":"mail-to-root"},
	"replication":{"max_workers":2},
	"tag-style":{"shape":"full","color-map":"prod:FF0000"},
	"u2f":{"appid":"https://pve.example:8006"},
	"webauthn":{"rp":"pve.example","origin":"https://pve.example:8006","id":"pve.example"},
	"user-tag-access":{"user-allow":"list","user-allow-list":["dev","test"]},
	"registered-tags":["prod","backup"],
	"allowed-tags":["dev","test","prod","backup"]
},"success":1}`

// clusterOptionsCaptured is a GET /cluster/options payload captured from a
// PVE 9.2 cluster with no property-string options set (`pve api get
// /cluster/options -o json`, 2026-09-03): only the scalar options and an
// empty allowed-tags list come back, and every unset option is absent
// rather than null or empty.
const clusterOptionsCaptured = `{"data":{
	"allowed-tags": [],
	"description": "",
	"keyboard": "en-us",
	"mac_prefix": "BC:24:11"
},"success":1}`

func TestListOptionsDecodesCapturedResponse(t *testing.T) {
	t.Parallel()

	srv, harness := newTestHarness()
	defer srv.Close()

	apiClient, err := pveclient.NewClient(smokeOptsFromServerURL(srv.URL))
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}

	harness.set(http.StatusOK, clusterOptionsCaptured)

	resp, err := cluster.New(apiClient).ListOptions(context.Background())
	if err != nil {
		t.Fatalf("ListOptions: unexpected error: %v", err)
	}

	if resp.Keyboard == nil || *resp.Keyboard != "en-us" {
		t.Errorf("Keyboard = %v, want en-us", resp.Keyboard)
	}

	if resp.MacPrefix == nil || *resp.MacPrefix != "BC:24:11" {
		t.Errorf("MacPrefix = %v, want BC:24:11", resp.MacPrefix)
	}

	if resp.Description == nil || *resp.Description != "" {
		t.Errorf("Description = %v, want an empty string, not nil", resp.Description)
	}

	if resp.AllowedTags == nil || len(resp.AllowedTags) != 0 {
		t.Errorf("AllowedTags = %#v, want an empty, non-nil list", resp.AllowedTags)
	}

	for name, raw := range map[string]json.RawMessage{"ha": resp.Ha, "migration": resp.Migration, "next-id": resp.NextId} {
		if raw != nil {
			t.Errorf("%s = %s, want absent", name, raw)
		}
	}

	if resp.RegisteredTags != nil {
		t.Errorf("RegisteredTags = %#v, want absent", resp.RegisteredTags)
	}
}

func TestListOptionsDecodesParsedDatacenterConfig(t *testing.T) {
	t.Parallel()

	srv, harness := newTestHarness()
	defer srv.Close()

	apiClient, err := pveclient.NewClient(smokeOptsFromServerURL(srv.URL))
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}

	harness.set(http.StatusOK, clusterOptionsBody)

	resp, err := cluster.New(apiClient).ListOptions(context.Background())
	if err != nil {
		t.Fatalf("ListOptions: unexpected error: %v", err)
	}

	assertScalarOptions(t, resp)
	assertObjectOptions(t, resp)
	assertTagOptions(t, resp)
}

// assertScalarOptions checks the plain options the spec types correctly.
func assertScalarOptions(t *testing.T, resp *cluster.ListOptionsResponse) {
	t.Helper()

	if resp.Keyboard == nil || *resp.Keyboard != "en-us" {
		t.Errorf("Keyboard = %v, want en-us", resp.Keyboard)
	}

	if resp.MaxWorkers == nil || int64(*resp.MaxWorkers) != 4 {
		t.Errorf("MaxWorkers = %v, want 4", resp.MaxWorkers)
	}
}

// assertObjectOptions checks the ten property-string options the spec
// declares as strings but the server sends as parsed objects.
func assertObjectOptions(t *testing.T, resp *cluster.ListOptionsResponse) {
	t.Helper()

	var haOption struct {
		ShutdownPolicy string `json:"shutdown_policy"`
	}

	decodeOption(t, "ha", resp.Ha, &haOption)

	if haOption.ShutdownPolicy != "migrate" {
		t.Errorf("ha.shutdown_policy = %q, want migrate", haOption.ShutdownPolicy)
	}

	var nextID struct {
		Lower int64 `json:"lower"`
		Upper int64 `json:"upper"`
	}

	decodeOption(t, "next-id", resp.NextId, &nextID)

	if nextID.Lower != 100 || nextID.Upper != 1000000 {
		t.Errorf("next-id = %+v, want lower=100 upper=1000000", nextID)
	}

	objects := map[string]json.RawMessage{
		"crs":             resp.Crs,
		"migration":       resp.Migration,
		"notify":          resp.Notify,
		"replication":     resp.Replication,
		"tag-style":       resp.TagStyle,
		"u2f":             resp.U2f,
		"user-tag-access": resp.UserTagAccess,
		"webauthn":        resp.Webauthn,
	}

	for name, raw := range objects {
		var obj map[string]json.RawMessage

		decodeOption(t, name, raw, &obj)

		if len(obj) == 0 {
			t.Errorf("%s = %s, want a non-empty JSON object", name, raw)
		}
	}
}

// assertTagOptions checks the two tag lists, one of which the spec
// declares as a string.
func assertTagOptions(t *testing.T, resp *cluster.ListOptionsResponse) {
	t.Helper()

	if got, want := resp.RegisteredTags, []string{"prod", "backup"}; !equalStrings(got, want) {
		t.Errorf("RegisteredTags = %v, want %v", got, want)
	}

	if got, want := resp.AllowedTags, []string{"dev", "test", "prod", "backup"}; !equalStrings(got, want) {
		t.Errorf("AllowedTags = %v, want %v", got, want)
	}
}

// decodeOption unmarshals one raw option into target, failing the test
// with the option's name when the payload is not the expected shape.
func decodeOption(t *testing.T, name string, raw json.RawMessage, target any) {
	t.Helper()

	err := json.Unmarshal(raw, target)
	if err != nil {
		t.Fatalf("%s = %s: %v", name, raw, err)
	}
}

func equalStrings(left, right []string) bool {
	if len(left) != len(right) {
		return false
	}

	for idx := range left {
		if left[idx] != right[idx] {
			return false
		}
	}

	return true
}
