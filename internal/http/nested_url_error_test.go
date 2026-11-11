package http //nolint:testpackage // white-box test: builds clients with the shared hostile helpers

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/fivetwenty-io/proxmox-apiclient-go/v3/internal/redirect/redirecttest"
)

var (
	// errWrappedReset is the cause the wrapping transport reports.
	errWrappedReset = errors.New("connection reset by peer")
	// errFirstEndpointDown is the failure the wrapper joins before its own.
	errFirstEndpointDown = errors.New("first endpoint down")
)

// urlErrorWrapper is a transport wrapper that sends through an inner client of
// its own and returns that client's *url.Error behind its own words, with the
// whole request URL in it.
type urlErrorWrapper struct{ join bool }

func (w urlErrorWrapper) RoundTrip(req *http.Request) (*http.Response, error) {
	inner := &url.Error{Op: "Get", URL: req.URL.String(), Err: errWrappedReset}
	if w.join {
		return nil, errors.Join(errFirstEndpointDown, inner)
	}

	return nil, fmt.Errorf("attempt 2 of 2: %w", inner)
}

// TestWrappedURLErrorLeavesNoQuerySecret sends a CIFS scan with a password in
// its query through a wrapper that returns a nested *url.Error. Neither the
// error nor any log line may hold the password.
func TestWrappedURLErrorLeavesNoQuerySecret(t *testing.T) {
	t.Parallel()

	for name, join := range map[string]bool{"percent w": false, "join": true} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			server := redirecttest.Serve(t, http.StatusOK, nil)
			client, captured := newHostileClient(t, server, func(o *Options) {
				o.WrapTransport = func(*http.Transport) http.RoundTripper { return urlErrorWrapper{join: join} }
			}, false)
			client.SetMaxRetries(0)

			params := map[string]interface{}{redactParamPassword: "QUERYSECRET", "domain": "x"}

			_, err := client.DoWithContext(context.Background(), http.MethodGet, "/nodes/n/scan/cifs", params)
			if err == nil {
				t.Fatal("got no error, want the wrapper's failure")
			}

			for _, text := range append(captured.all(), err.Error(), fmt.Sprintf("%+v", err)) {
				if strings.Contains(text, "QUERYSECRET") {
					t.Errorf("%q holds the query secret", text)
				}
			}

			if !errors.Is(err, errWrappedReset) {
				t.Errorf("error = %v, want the wrapper's cause kept", err)
			}
		})
	}
}
