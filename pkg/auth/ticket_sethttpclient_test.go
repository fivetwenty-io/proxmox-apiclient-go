package auth_test

import (
	"net/http"
	"net/url"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/fivetwenty-io/proxmox-apiclient-go/v3/pkg/auth"
)

// countingRoundTripper counts the requests it hands to its inner transport.
type countingRoundTripper struct {
	inner http.RoundTripper
	calls atomic.Int32
}

func (c *countingRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	c.calls.Add(1)

	return c.inner.RoundTrip(req) //nolint:wrapcheck // a transport must hand back the inner error untouched
}

// newCountingTicketAuth returns a ticket authenticator for the mock server
// whose http.Client counts its requests.
func newCountingTicketAuth(t *testing.T) (*auth.TicketAuthenticator, *countingRoundTripper, *http.Client) {
	t.Helper()

	srv := newMockPVEServer(t)
	t.Cleanup(srv.Close)

	u, err := url.Parse(srv.URL)
	if err != nil {
		t.Fatalf("parse server URL: %v", err)
	}

	baseURL := u.Scheme + "://" + u.Host + "/api2/json"

	counter := &countingRoundTripper{inner: srv.Client().Transport}
	httpClient := &http.Client{Transport: counter}
	creds := &auth.Credentials{Username: testUserRoot, Password: testSecretPass, Realm: testRealm}

	return auth.NewTicketAuthenticator(baseURL, creds, httpClient, "", false), counter, httpClient
}

// TestTicketAuthenticator_SetHTTPClientSendsOnReplacement verifies a login
// after SetHTTPClient goes through the new client, and a nil client is ignored.
func TestTicketAuthenticator_SetHTTPClientSendsOnReplacement(t *testing.T) {
	t.Parallel()

	ticketAuth, first, _ := newCountingTicketAuth(t)

	err := ticketAuth.Authenticate()
	if err != nil {
		t.Fatalf("Authenticate() with the first client: %v", err)
	}

	second := &countingRoundTripper{inner: first.inner}
	ticketAuth.SetHTTPClient(&http.Client{Transport: second})
	ticketAuth.SetHTTPClient(nil)

	err = ticketAuth.RefreshForce()
	if err != nil {
		t.Fatalf("RefreshForce() after SetHTTPClient: %v", err)
	}

	if got := first.calls.Load(); got != 1 {
		t.Errorf("first client sent %d requests, want 1", got)
	}

	if got := second.calls.Load(); got != 1 {
		t.Errorf("replacement client sent %d requests, want 1 (a nil SetHTTPClient must not replace it)", got)
	}
}

// TestTicketAuthenticator_SetHTTPClientConcurrentWithLogin verifies
// SetHTTPClient is safe while logins run.
func TestTicketAuthenticator_SetHTTPClientConcurrentWithLogin(t *testing.T) {
	t.Parallel()

	ticketAuth, _, httpClient := newCountingTicketAuth(t)

	const goroutines = 20

	var waitGroup sync.WaitGroup

	for id := range goroutines {
		waitGroup.Go(func() {
			if id%2 == 0 {
				ticketAuth.SetHTTPClient(&http.Client{Transport: httpClient.Transport})

				return
			}

			refreshErr := ticketAuth.RefreshForce()
			if refreshErr != nil {
				t.Errorf("RefreshForce(): %v", refreshErr)
			}
		})
	}

	waitGroup.Wait()
}
