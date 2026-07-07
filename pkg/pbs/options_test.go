package pbs_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"testing"

	"github.com/fivetwenty-io/proxmox-apiclient-go/v3/pkg/client"
	"github.com/fivetwenty-io/proxmox-apiclient-go/v3/pkg/pbs"
	"github.com/fivetwenty-io/proxmox-apiclient-go/v3/pkg/pbs/version"
)

const testHost = "backup.example.com"

func TestDefaultOptions_FillsPBSDefaults(t *testing.T) {
	t.Parallel()

	got := pbs.DefaultOptions(client.Options{Host: testHost})

	if got.Port != pbs.DefaultPort {
		t.Errorf("Port = %d, want %d", got.Port, pbs.DefaultPort)
	}

	if got.APITokenName != pbs.APITokenName {
		t.Errorf("APITokenName = %q, want %q", got.APITokenName, pbs.APITokenName)
	}

	if got.CookieName != pbs.CookieName {
		t.Errorf("CookieName = %q, want %q", got.CookieName, pbs.CookieName)
	}

	if got.Host != testHost {
		t.Errorf("Host = %q, want passthrough", got.Host)
	}
}

func TestDefaultOptions_PreservesExplicitValues(t *testing.T) {
	t.Parallel()

	base := client.Options{
		Host:         testHost,
		Port:         9999,
		APITokenName: "CustomToken",
		CookieName:   "CustomCookie",
	}

	got := pbs.DefaultOptions(base)

	if got.Port != 9999 {
		t.Errorf("Port = %d, want explicit 9999 preserved", got.Port)
	}

	if got.APITokenName != "CustomToken" {
		t.Errorf("APITokenName = %q, want explicit value preserved", got.APITokenName)
	}

	if got.CookieName != "CustomCookie" {
		t.Errorf("CookieName = %q, want explicit value preserved", got.CookieName)
	}
}

// TestNewClient_SendsPBSAPITokenHeader proves the preset wires the PBS
// token name all the way onto the Authorization header of a real
// request issued through a generated pkg/pbs service.
func TestNewClient_SendsPBSAPITokenHeader(t *testing.T) {
	t.Parallel()

	var gotAuth string

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":{"release":"1","repoid":"abc","version":"4.0"}}`))
	}))
	defer srv.Close()

	parsed, err := url.Parse(srv.URL)
	if err != nil {
		t.Fatalf("parse server URL: %v", err)
	}

	port, err := strconv.Atoi(parsed.Port())
	if err != nil {
		t.Fatalf("parse server port: %v", err)
	}

	cli, err := pbs.NewClient(client.Options{
		Host:     parsed.Hostname(),
		Port:     port,
		Protocol: "http",
		APIToken: "user@pbs!tok=secret",
	})
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}

	_, err = version.New(cli).Get(context.Background())
	if err != nil {
		t.Fatalf("version.Get: %v", err)
	}

	const wantPrefix = "PBSAPIToken=user@pbs!tok"
	if !strings.HasPrefix(gotAuth, wantPrefix) {
		t.Errorf("Authorization = %q, want prefix %q", gotAuth, wantPrefix)
	}
}

// TestNewClient_DefaultPortInBaseURL proves an options value without an
// explicit port targets 8007 rather than PVE's 8006.
func TestNewClient_DefaultPortInBaseURL(t *testing.T) {
	t.Parallel()

	opts := pbs.DefaultOptions(client.Options{
		Host:     testHost,
		APIToken: "user@pbs!tok=secret",
	})

	err := opts.Validate()
	if err != nil {
		t.Fatalf("Validate: %v", err)
	}

	if !strings.Contains(opts.GetBaseURL(), ":8007/") {
		t.Errorf("GetBaseURL() = %q, want port 8007", opts.GetBaseURL())
	}
}
