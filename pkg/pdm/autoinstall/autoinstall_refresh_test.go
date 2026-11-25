package autoinstall_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"testing"

	pveclient "github.com/fivetwenty-io/proxmox-apiclient-go/v3/pkg/client"
	"github.com/fivetwenty-io/proxmox-apiclient-go/v3/pkg/pdm/autoinstall"
)

const bondModeLACP = "802.3ad"

//nolint:ireturn // Test helper returns the package public interface.
func newRefreshService(t *testing.T, body string, form *url.Values) autoinstall.Service {
	t.Helper()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		*form = r.PostForm

		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)

	parsed, err := url.Parse(srv.URL)
	if err != nil {
		t.Fatalf("parse server URL: %v", err)
	}

	port, _ := strconv.Atoi(parsed.Port())

	client, err := pveclient.NewClient(pveclient.Options{
		Host: parsed.Hostname(), Port: port, Protocol: "http", APIToken: "user@pam!tok=sec",
	})
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}

	return autoinstall.New(client)
}

func TestCreatePreparedSendsBondAndVlanFields(t *testing.T) {
	t.Parallel()

	var form url.Values

	svc := newRefreshService(t, `{"data":{},"success":1}`, &form)

	enabled := true
	mode := bondModeLACP
	policy := "layer2+3"
	match := "any"
	vlan := int64(42)

	_, err := svc.CreatePrepared(context.Background(), &autoinstall.CreatePreparedParams{
		Country: "us", DiskMode: "fixed", Filesystem: json.RawMessage(`{"ext4":{}}`), Fqdn: "pve1.example.com",
		Id: "rack-a", Keyboard: "en-us", Mailto: "root@example.com", NetifNamePinningEnabled: true,
		RebootMode: "reboot", Timezone: "UTC", UseDhcpNetwork: false,
		BondEnabled: &enabled, BondMode: &mode, BondHashPolicy: &policy, BondMembersFilterMatch: &match,
		BondMembersFilter: json.RawMessage(`{"ID_NET_NAME":"enp*"}`), VlanId: &vlan,
	})
	if err != nil {
		t.Fatalf("CreatePrepared: %v", err)
	}

	want := map[string]string{
		"bond-enabled":              "1",
		"bond-mode":                 bondModeLACP,
		"bond-hash-policy":          "layer2+3",
		"bond-members-filter-match": "any",
		"bond-members-filter":       `{"ID_NET_NAME":"enp*"}`,
		"vlan-id":                   "42",
		"id":                        "rack-a",
	}
	for key, value := range want {
		if got := form.Get(key); got != value {
			t.Errorf("form %q = %q, want %q", key, got, value)
		}
	}
}

func TestGetPreparedKeepsBondAndVlanFields(t *testing.T) {
	t.Parallel()

	var form url.Values

	payload := `{"id":"rack-a","fqdn":"pve1.example.com","bond-enabled":true,"bond-mode":"802.3ad",` +
		`"bond-hash-policy":"layer2+3","bond-members-filter-match":"any",` +
		`"bond-members-filter":{"ID_NET_NAME":"enp*"},"vlan-id":42,"disk-list":["sda","sdb"]}`

	svc := newRefreshService(t, `{"data":`+payload+`,"success":1}`, &form)

	resp, err := svc.GetPrepared(context.Background(), "rack-a")
	if err != nil {
		t.Fatalf("GetPrepared: %v", err)
	}

	var decoded struct {
		BondEnabled bool              `json:"bond-enabled"`
		BondMode    string            `json:"bond-mode"`
		Filter      map[string]string `json:"bond-members-filter"`
		VlanID      int64             `json:"vlan-id"`
		DiskList    []string          `json:"disk-list"`
	}

	err = json.Unmarshal(*resp, &decoded)
	if err != nil {
		t.Fatalf("decode prepared config: %v", err)
	}

	if !decoded.BondEnabled || decoded.BondMode != bondModeLACP || decoded.VlanID != 42 ||
		decoded.Filter["ID_NET_NAME"] != "enp*" || len(decoded.DiskList) != 2 {
		t.Errorf("unexpected decoded config: %+v", decoded)
	}
}
