package auth_test

import (
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/fivetwenty-io/proxmox-apiclient-go/v3/pkg/auth"
	apierrors "github.com/fivetwenty-io/proxmox-apiclient-go/v3/pkg/errors"
)

// failingLoginServer returns an httptest server whose /access/ticket endpoint
// always answers with the given status and body, simulating pveproxy (or a
// reverse proxy in front of it) failing the login itself.
func failingLoginServer(t *testing.T, status int, body string) *httptest.Server {
	t.Helper()

	srv := httptest.NewServer(http.HandlerFunc(func(respWriter http.ResponseWriter, httpReq *http.Request) {
		if httpReq.Method == http.MethodPost && httpReq.URL.Path == pathAccessTicket {
			respWriter.WriteHeader(status)
			_, _ = io.WriteString(respWriter, body)

			return
		}

		http.NotFound(respWriter, httpReq)
	}))
	t.Cleanup(srv.Close)

	return srv
}

func newTicketAuthAgainst(srv *httptest.Server) *auth.TicketAuthenticator {
	creds := &auth.Credentials{Username: testUserRoot, Password: testSecretPass, Realm: testRealm}

	return auth.NewTicketAuthenticator(buildBaseURL(srv.URL), creds, srv.Client(), "", false)
}

// TestAuthenticate_ServerFailurePreservesStatusChain verifies that a 5xx login
// response surfaces BOTH the historical no-ticket sentinel (so existing
// errors.Is consumers keep working) and the HTTP-status classification chain
// from ParseAPIError (so retriability classifiers can see the 5xx). Before the
// fix, Authenticate discarded AuthResult.Error and returned the bare sentinel,
// so the ErrServer assertion below could never pass.
func TestAuthenticate_ServerFailurePreservesStatusChain(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name   string
		status int
		body   string
	}{
		{"503 json", http.StatusServiceUnavailable, `{"message":"service unavailable"}`},
		{"596 proxy", 596, "<html>connection timed out</html>"},
		{"500 html", http.StatusInternalServerError, "<html>internal error</html>"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			srv := failingLoginServer(t, tc.status, tc.body)
			ticketAuth := newTicketAuthAgainst(srv)

			err := ticketAuth.Authenticate()
			if err == nil {
				t.Fatalf("Authenticate() = nil, want an error for a %d login response", tc.status)
			}

			if !errors.Is(err, auth.ErrAuthenticationFailedNoTicket) {
				t.Errorf("errors.Is(err, ErrAuthenticationFailedNoTicket) = false, want true; err = %v", err)
			}

			if !errors.Is(err, apierrors.ErrServer) {
				t.Errorf("errors.Is(err, apierrors.ErrServer) = false, want true (the 5xx chain must survive); err = %v", err)
			}
		})
	}
}

// TestRefreshForce_ServerFailurePreservesStatusChain is the RefreshForce twin:
// a forced renewal that dies on a 5xx must keep the status chain visible.
func TestRefreshForce_ServerFailurePreservesStatusChain(t *testing.T) {
	t.Parallel()

	srv := failingLoginServer(t, http.StatusServiceUnavailable, `{"message":"pveproxy restarting"}`)
	ticketAuth := newTicketAuthAgainst(srv)

	err := ticketAuth.RefreshForce()
	if err == nil {
		t.Fatal("RefreshForce() = nil, want an error for a 503 login response")
	}

	if !errors.Is(err, auth.ErrAuthenticationFailedNoTicket) {
		t.Errorf("errors.Is(err, ErrAuthenticationFailedNoTicket) = false, want true; err = %v", err)
	}

	if !errors.Is(err, apierrors.ErrServer) {
		t.Errorf("errors.Is(err, apierrors.ErrServer) = false, want true (the 5xx chain must survive); err = %v", err)
	}
}

// TestAuthenticate_NoTicket2xx_KeepsSentinelShape pins the pre-existing
// behavior for a well-formed 200 that simply carries no ticket: the sentinel
// still fires and no server-error classification appears (there was no server
// error).
func TestAuthenticate_NoTicket2xx_KeepsSentinelShape(t *testing.T) {
	t.Parallel()

	srv := failingLoginServer(t, http.StatusOK, `{"data":{}}`)
	ticketAuth := newTicketAuthAgainst(srv)

	err := ticketAuth.Authenticate()
	if err == nil {
		t.Fatal("Authenticate() = nil, want an error when no ticket is returned")
	}

	if !errors.Is(err, auth.ErrAuthenticationFailedNoTicket) {
		t.Errorf("errors.Is(err, ErrAuthenticationFailedNoTicket) = false, want true; err = %v", err)
	}

	if errors.Is(err, apierrors.ErrServer) {
		t.Errorf("errors.Is(err, apierrors.ErrServer) = true, want false for a clean 200 without a ticket; err = %v", err)
	}
}
