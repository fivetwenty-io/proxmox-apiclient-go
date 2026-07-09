package access_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"testing"

	pveclient "github.com/fivetwenty-io/proxmox-apiclient-go/v3/pkg/client"
	"github.com/fivetwenty-io/proxmox-apiclient-go/v3/pkg/pbs/access"
)

// newNilDataService starts a mock server that returns body for every
// request and wires up a Service pointed at it. Reproduced locally (rather
// than reusing access_smoke_test.go's newTestHarness/smokeOptsFromServerURL)
// so this file has no dependency on a generated file's internals surviving
// regeneration; the fake token matches the one already used there.
//nolint:ireturn // mirrors access.New's own interface return; there is no concrete type to return instead
func newNilDataService(t *testing.T, body string) access.Service {
	t.Helper()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)

	parsed, err := url.Parse(srv.URL)
	if err != nil {
		t.Fatalf("parse server URL: %v", err)
	}

	port, _ := strconv.Atoi(parsed.Port())

	apiClient, err := pveclient.NewClient(pveclient.Options{
		Host:     parsed.Hostname(),
		Port:     port,
		Protocol: "http",
		APIToken: "user@pam!tok=sec",
	})
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}

	return access.New(apiClient)
}

// TestNilDataResponseBranches exercises, over a real HTTP round trip, the
// two branches renderMethodBodyResponse (cmd/pvegen) emits when resp.Data
// is nil. Endpoints where isResponseEmptyOk is true (array/aliased-RawMessage
// responses, e.g. ListAcl) take the tolerant branch and return a zero-value
// response with no error; endpoints with a populated, required object
// return schema (e.g. GetUsersToken) take the strict branch and return an
// error. Neither branch was previously exercised at runtime — the generated
// smoke tests always send a non-nil "data" value.
func TestNilDataResponseBranches(t *testing.T) {
	t.Parallel()

	t.Run("tolerant branch: data is null", func(t *testing.T) {
		t.Parallel()

		svc := newNilDataService(t, `{"data":null,"success":1}`)

		resp, err := svc.ListAcl(context.Background(), &access.ListAclParams{})
		if err != nil {
			t.Fatalf("ListAcl: unexpected error: %v", err)
		}

		if resp == nil || len(*resp) != 0 {
			t.Fatalf("ListAcl: response = %#v, want a non-nil empty response", resp)
		}
	})

	t.Run("tolerant branch: no data key at all", func(t *testing.T) {
		t.Parallel()

		svc := newNilDataService(t, `{"success":1}`)

		resp, err := svc.ListAcl(context.Background(), &access.ListAclParams{})
		if err != nil {
			t.Fatalf("ListAcl: unexpected error: %v", err)
		}

		if resp == nil || len(*resp) != 0 {
			t.Fatalf("ListAcl: response = %#v, want a non-nil empty response", resp)
		}
	})

	t.Run("strict branch: null data is a hard error", func(t *testing.T) {
		t.Parallel()

		svc := newNilDataService(t, `{"data":null,"success":1}`)

		_, err := svc.GetUsersToken(context.Background(), "user@pam", "tok")
		if err == nil {
			t.Fatal("GetUsersToken: expected an error for nil data, got nil")
		}

		if !strings.Contains(err.Error(), "empty data") {
			t.Errorf("GetUsersToken: error = %q, want it to mention %q", err.Error(), "empty data")
		}
	})
}
