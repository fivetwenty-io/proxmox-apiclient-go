package http //nolint:testpackage // white-box: exercises classifyTransportError and performAutoLogin directly

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/fivetwenty-io/proxmox-apiclient-go/v3/pkg/auth"
	apierrors "github.com/fivetwenty-io/proxmox-apiclient-go/v3/pkg/errors"
)

// Static stand-ins for the chainless stdlib sentinels under test.
var (
	errServerClosedIdleText = errors.New(serverClosedIdleMessage)
	errIdleLookalike        = errors.New("pveproxy tuning: http: server closed idle connection limits were adjusted")
)

// dropTestRequest builds a minimal request for classifyTransportError, which
// only reads the URL for host and port.
func dropTestRequest(t *testing.T) *http.Request {
	t.Helper()

	req, err := http.NewRequestWithContext(
		context.Background(), http.MethodPost, "http://pve.example.com:8006/api2/json/nodes", nil,
	)
	if err != nil {
		t.Fatalf("NewRequestWithContext: %v", err)
	}

	return req
}

// TestClassifyTransportError_DropSentinels_TypedConnectionError verifies that
// the two connection-lifecycle races the stdlib surfaces as bare, chainless
// sentinels (the server closing an idle keep-alive connection, and a
// client-side use of an already-closed connection) come out of the retry loop
// as the typed *ConnectionError every SDK consumer classifies on, instead of
// as an anonymous fmt wrap. Before the fix these surfaced raw.
func TestClassifyTransportError_DropSentinels_TypedConnectionError(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name string
		err  error
	}{
		{
			name: "server closed idle connection",
			err:  &url.Error{Op: "Post", URL: "http://pve.example.com:8006/api2/json/nodes", Err: errServerClosedIdleText},
		},
		{
			name: "net.ErrClosed via url.Error",
			err:  &url.Error{Op: "Post", URL: "http://pve.example.com:8006/api2/json/nodes", Err: net.ErrClosed},
		},
		{
			name: "net.ErrClosed nested deeper",
			err:  fmt.Errorf("round trip: %w", &net.OpError{Op: "write", Err: net.ErrClosed}),
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			// retryAllowed=false models the non-idempotent POST path where the
			// error surfaces on the first attempt.
			surfaced := classifyTransportError(dropTestRequest(t), tc.err, 0, 3, false)
			if surfaced == nil {
				t.Fatal("classifyTransportError = nil, want a surfaced error when retry is not allowed")
			}

			var connErr *apierrors.ConnectionError
			if !errorsAs(surfaced, &connErr) {
				t.Errorf("surfaced error = %v, want a *apierrors.ConnectionError", surfaced)
			}

			if !errors.Is(surfaced, tc.err) {
				t.Errorf("surfaced error must keep the cause chain; errors.Is(surfaced, cause) = false for %v", surfaced)
			}
		})
	}
}

// TestClassifyTransportError_DropSentinels_StillRetriedWhenAllowed pins the
// retry semantics: a connection drop on an idempotent request keeps riding the
// SDK retry loop (return nil means retry). The typed mapping only changes what
// surfaces, never whether the loop retries.
func TestClassifyTransportError_DropSentinels_StillRetriedWhenAllowed(t *testing.T) {
	t.Parallel()

	dropErr := &url.Error{Op: "Get", URL: "http://pve.example.com:8006/api2/json/version", Err: errServerClosedIdleText}

	surfaced := classifyTransportError(dropTestRequest(t), dropErr, 0, 3, true)
	if surfaced != nil {
		t.Errorf("classifyTransportError = %v, want nil (retry) for an idempotent request with attempts left", surfaced)
	}
}

// TestClassifyTransportError_SubstringDoesNotMatch guards the allow-list
// discipline: the sentinel is matched per unwrap link by full-string equality,
// so a different error that merely mentions idle connections must keep the
// existing anonymous-wrap shape.
func TestClassifyTransportError_SubstringDoesNotMatch(t *testing.T) {
	t.Parallel()

	lookalike := errIdleLookalike

	surfaced := classifyTransportError(dropTestRequest(t), lookalike, 0, 3, false)
	if surfaced == nil {
		t.Fatal("classifyTransportError = nil, want a surfaced error")
	}

	var connErr *apierrors.ConnectionError
	if errorsAs(surfaced, &connErr) {
		t.Errorf("surfaced error = %v, want NO *apierrors.ConnectionError for a substring lookalike", surfaced)
	}
}

// TestAutoLogin_FailedLoginRetriesWithSamePrefix verifies two things about the
// auto-login path when the login itself keeps failing (pveproxy down, 503):
// the first and second request through the same client both attempt a login,
// and both surface the same "auto-login failed" prefix. Before the fix,
// loginAttempted stayed set after a failed login, so the second request
// silently skipped auto-login and surfaced a differently shaped error from
// the 401-retry path.
func TestAutoLogin_FailedLoginRetriesWithSamePrefix(t *testing.T) {
	t.Parallel()

	srv := newTestServer(t, func(respWriter http.ResponseWriter, httpReq *http.Request) {
		if httpReq.Method == http.MethodPost && strings.Contains(httpReq.URL.Path, "/access/ticket") {
			respWriter.WriteHeader(http.StatusServiceUnavailable)
			_, _ = respWriter.Write([]byte(`{"message":"service unavailable"}`))

			return
		}

		// Anything else is unauthenticated.
		respWriter.WriteHeader(http.StatusUnauthorized)
		_, _ = respWriter.Write([]byte(`{"message":"authentication required"}`))
	})

	opts := minimalHTTPOptions()
	opts.Username = testUsername
	opts.Password = testPassword
	opts.AutoLogin = true

	client, err := NewClient(opts)
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}

	client.baseURL = srv.URL
	client.authenticator = auth.NewTicketAuthenticator(
		srv.URL+"/api2/json",
		&auth.Credentials{Username: "root", Password: testPassword, Realm: "pam"},
		srv.Client(),
		"",
		false,
	)

	_, firstErr := client.Do("GET", "/version", nil)
	if firstErr == nil {
		t.Fatal("first Do() = nil error, want an auto-login failure")
	}

	if !strings.Contains(firstErr.Error(), "auto-login failed") {
		t.Errorf("first error = %v, want the auto-login failed prefix", firstErr)
	}

	_, secondErr := client.Do("GET", "/version", nil)
	if secondErr == nil {
		t.Fatal("second Do() = nil error, want an auto-login failure")
	}

	if !strings.Contains(secondErr.Error(), "auto-login failed") {
		t.Errorf("second error = %v, want the same auto-login failed prefix as the first attempt", secondErr)
	}
}
