package plugins_test

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"

	"github.com/fivetwenty-io/proxmox-apiclient-go/v3/pkg/api/plugins"
	pveclient "github.com/fivetwenty-io/proxmox-apiclient-go/v3/pkg/client"
)

// storagePluginBody is a GET /plugins/storage/plugin/{type} payload in the
// shape the PVE 9.3 spec declares: content lists the default and supported
// content types, and schema carries the plugin's property schema.
const storagePluginBody = `{"data":{
	"type":"nvmeof",
	"origin":"builtin",
	"display-name":"NVMe/TCP",
	"content":{"default":["images"],"supported":["images","rootdir"]},
	"schema":{"portals":{"type":"string"},"subsysnqn":{"type":"string"}}
},"success":1}`

//nolint:ireturn // test helper returns the Service interface that plugins.New returns
func newPluginsService(t *testing.T) (plugins.Service, *testHarness) {
	t.Helper()

	srv, harness := newTestHarness()
	t.Cleanup(srv.Close)

	apiClient, err := pveclient.NewClient(smokeOptsFromServerURL(srv.URL))
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}

	return plugins.New(apiClient), harness
}

func TestGetStoragePluginDecodesMetadata(t *testing.T) {
	t.Parallel()

	svc, harness := newPluginsService(t)
	harness.set(http.StatusOK, storagePluginBody)

	resp, err := svc.GetStoragePlugin(context.Background(), "nvmeof")
	if err != nil {
		t.Fatalf("GetStoragePlugin: unexpected error: %v", err)
	}

	assertRequestLine(t, harness.snapshot(), http.MethodGet, "/api2/json/plugins/storage/plugin/nvmeof")

	if resp.Type != "nvmeof" || resp.Origin != "builtin" {
		t.Errorf("response = %+v, want the builtin nvmeof plugin", resp)
	}

	if resp.DisplayName == nil || *resp.DisplayName != "NVMe/TCP" {
		t.Errorf("DisplayName = %v, want NVMe/TCP", resp.DisplayName)
	}

	var content struct {
		Default   []string `json:"default"`
		Supported []string `json:"supported"`
	}

	err = json.Unmarshal(resp.Content, &content)
	if err != nil {
		t.Fatalf("content = %s: %v", resp.Content, err)
	}

	if len(content.Default) != 1 || content.Default[0] != "images" || len(content.Supported) != 2 {
		t.Errorf("content = %+v, want default [images] and two supported types", content)
	}

	var pluginSchema map[string]json.RawMessage

	err = json.Unmarshal(resp.Schema, &pluginSchema)
	if err != nil {
		t.Fatalf("schema = %s: %v", resp.Schema, err)
	}

	if _, ok := pluginSchema["subsysnqn"]; !ok {
		t.Errorf("schema = %s, want the subsysnqn property", resp.Schema)
	}
}

func TestListStoragePluginSendsOrigin(t *testing.T) {
	t.Parallel()

	svc, harness := newPluginsService(t)
	harness.set(http.StatusOK, `{"data":[{"type":"dir","origin":"builtin"},{"type":"nvmeof","origin":"builtin"}],"success":1}`)

	origin := "builtin"

	resp, err := svc.ListStoragePlugin(context.Background(), &plugins.ListStoragePluginParams{Origin: &origin})
	if err != nil {
		t.Fatalf("ListStoragePlugin: unexpected error: %v", err)
	}

	got := harness.snapshot()
	assertRequestLine(t, got, http.MethodGet, "/api2/json/plugins/storage/plugin")
	assertParamValue(t, got.query, "origin", origin)

	if resp == nil || len(*resp) != 2 {
		t.Errorf("ListStoragePlugin returned %v, want two plugins", resp)
	}
}
