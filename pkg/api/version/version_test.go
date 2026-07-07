package version_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"testing"

	"github.com/fivetwenty-io/proxmox-apiclient-go/v3/pkg/api/version"
	pveclient "github.com/fivetwenty-io/proxmox-apiclient-go/v3/pkg/client"
)

// optsFromServerURL converts an httptest.Server URL into a pkg/client.Options
// configured for plain HTTP and API-token auth (so the test bypasses login).
func optsFromServerURL(u string) pveclient.Options {
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

func TestVersionGet_DecodesTypedResponse(t *testing.T) {
	t.Parallel()

	mux := http.NewServeMux()
	mux.HandleFunc("/api2/json/version", func(respWriter http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.Error(respWriter, "method", http.StatusMethodNotAllowed)

			return
		}

		_ = json.NewEncoder(respWriter).Encode(map[string]any{
			"data": map[string]any{
				"release": "9.0",
				"repoid":  "deadbeef",
				"version": "9.0.3",
				"console": "xtermjs",
			},
			"success": 1,
		})
	})

	srv := httptest.NewServer(mux)
	defer srv.Close()

	c, err := pveclient.NewClient(optsFromServerURL(srv.URL))
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}

	svc := version.New(c)

	got, err := svc.Get(context.Background())
	if err != nil {
		t.Fatalf("Get: %v", err)
	}

	if got == nil {
		t.Fatal("Get returned nil response")
	}

	if got.Release != "9.0" {
		t.Errorf("Release = %q, want %q", got.Release, "9.0")
	}

	if got.Repoid != "deadbeef" {
		t.Errorf("Repoid = %q, want %q", got.Repoid, "deadbeef")
	}

	if got.Version != "9.0.3" {
		t.Errorf("Version = %q, want %q", got.Version, "9.0.3")
	}

	if got.Console == nil {
		t.Fatal("Console pointer is nil; expected xtermjs")
	}

	if *got.Console != "xtermjs" {
		t.Errorf("Console = %q, want %q", *got.Console, "xtermjs")
	}
}

func TestVersionGet_OmittedOptionalIsNil(t *testing.T) {
	t.Parallel()

	mux := http.NewServeMux()
	mux.HandleFunc("/api2/json/version", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"data": map[string]any{
				"release": "9.1",
				"repoid":  "cafef00d",
				"version": "9.1.0",
			},
			"success": 1,
		})
	})

	srv := httptest.NewServer(mux)
	defer srv.Close()

	c, err := pveclient.NewClient(optsFromServerURL(srv.URL))
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}

	got, err := version.New(c).Get(context.Background())
	if err != nil {
		t.Fatalf("Get: %v", err)
	}

	if got.Console != nil {
		t.Errorf("Console = %v, want nil (omitted optional)", *got.Console)
	}
}

func TestVersionGet_NilContextRejected(t *testing.T) {
	t.Parallel()

	// We construct a real client but no server is required because the
	// nil-context check fires before any transport call.
	versionClient, err := pveclient.NewClient(pveclient.Options{
		Host: "127.0.0.1", Port: 1, Protocol: "http", APIToken: "u@pam!t=s",
	})
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	// A nil context typed through a variable exercises the runtime
	// validation without tripping the static "nil literal" check.
	var nilCtx context.Context

	_, err = version.New(versionClient).Get(nilCtx)
	if err == nil {
		t.Fatal("expected error for nil context, got nil")
	}

	if !strings.Contains(err.Error(), "ctx") {
		t.Errorf("error %q does not mention ctx", err.Error())
	}
}

func TestVersionGet_TransportErrorPropagates(t *testing.T) {
	t.Parallel()

	mux := http.NewServeMux()
	mux.HandleFunc("/api2/json/version", func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "internal", http.StatusInternalServerError)
	})

	srv := httptest.NewServer(mux)
	defer srv.Close()

	c, err := pveclient.NewClient(optsFromServerURL(srv.URL))
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}

	_, err = version.New(c).Get(context.Background())
	if err == nil {
		t.Fatal("expected error for 500 response, got nil")
	}
}

func TestVersionNew_NilClientPanics(t *testing.T) {
	t.Parallel()

	defer func() {
		if r := recover(); r == nil {
			t.Fatal("expected panic for nil client, got none")
		}
	}()

	_ = version.New(nil)
}
