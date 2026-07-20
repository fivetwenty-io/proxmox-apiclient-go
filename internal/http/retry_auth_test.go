package http //nolint:testpackage // white-box test: accesses unexported client fields and middleware

import (
	"context"
	"errors"
	"io"
	"net/http"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/fivetwenty-io/proxmox-apiclient-go/v3/pkg/auth"
	apierrors "github.com/fivetwenty-io/proxmox-apiclient-go/v3/pkg/errors"
)

// errorsAs is a thin wrapper so the test reads clearly.
func errorsAs(err error, target interface{}) bool {
	return errors.As(err, target)
}

// TestRetryMiddleware_NoAutoRetryPOSTOn5xx verifies that non-idempotent methods
// (POST) are NOT auto-retried on retryable status codes. Auto-retrying a POST
// could duplicate side effects such as VM create/clone.
func TestRetryMiddleware_NoAutoRetryPOSTOn5xx(t *testing.T) {
	t.Parallel()

	var calls int32

	srv := newTestServer(t, func(writer http.ResponseWriter, _ *http.Request) {
		atomic.AddInt32(&calls, 1)
		writer.WriteHeader(http.StatusServiceUnavailable)
		_, _ = writer.Write([]byte(`{"message":"unavailable"}`))
	})

	client := clientPointedAt(t, srv.URL)
	client.maxRetries = 3
	client.retryDelay = time.Millisecond

	_, _ = client.Do("POST", "/nodes/n1/qemu", map[string]interface{}{keyVMID: 100})

	if got := atomic.LoadInt32(&calls); got != 1 {
		t.Errorf("calls = %d, want 1 (POST must not auto-retry on 5xx)", got)
	}
}

// TestRetryMiddleware_NoAutoRetryPOSTOnNetworkError verifies that a POST is not
// retried when the underlying transport returns a network error.
func TestRetryMiddleware_NoAutoRetryPOSTOnNetworkError(t *testing.T) {
	t.Parallel()

	var calls int32

	// A server that hangs up mid-response produces a POST-CONNECT network error.
	// A refused dial would not work here: those are terminal for every method
	// (see isTerminalTransportError), so the test would pass without proving
	// anything about POST.
	srv := newTestServer(t, func(writer http.ResponseWriter, _ *http.Request) {
		hijacker, ok := writer.(http.Hijacker)
		if !ok {
			t.Error("test server does not support hijacking")

			return
		}

		conn, _, hijackErr := hijacker.Hijack()
		if hijackErr != nil {
			t.Errorf("hijack: %v", hijackErr)

			return
		}

		_ = conn.Close()
	})

	client := countingClient(t, srv.URL, &calls)

	_, _ = client.Do("POST", "/nodes/n1/qemu", map[string]interface{}{keyVMID: 100})

	if got := atomic.LoadInt32(&calls); got != 1 {
		t.Errorf("calls = %d, want 1 (POST must not auto-retry on network error)", got)
	}
}

// TestRetryMiddleware_POSTOptInRetryResendsBody verifies that when a caller
// explicitly opts in to retrying a POST, the request body is resent intact on
// each attempt (not drained to empty after the first send).
func TestRetryMiddleware_POSTOptInRetryResendsBody(t *testing.T) {
	t.Parallel()

	var (
		calls    int32
		bodies   = make([]string, 0, 4)
		bodiesMu sync.Mutex
	)

	srv := newTestServer(t, func(writer http.ResponseWriter, r *http.Request) {
		callNum := atomic.AddInt32(&calls, 1)

		b, _ := io.ReadAll(r.Body)

		bodiesMu.Lock()

		bodies = append(bodies, string(b))
		bodiesMu.Unlock()

		if callNum < 2 {
			writer.WriteHeader(http.StatusServiceUnavailable)

			return
		}

		writer.Header().Set("Content-Type", "application/json")
		_, _ = writer.Write(pveEnvelope(t, "ok"))
	})

	client := clientPointedAt(t, srv.URL)
	client.maxRetries = 0
	client.retryDelay = time.Millisecond

	ctx := WithForceRetry(context.Background(), true)
	ctx = WithRetries(ctx, 3)
	ctx = WithRetryDelay(ctx, time.Millisecond)

	_, err := client.DoWithContext(ctx, "POST", "/nodes/n1/qemu", map[string]interface{}{keyVMID: 100})
	if err != nil {
		t.Fatalf("expected success after opt-in retry, got: %v", err)
	}

	if got := atomic.LoadInt32(&calls); got < 2 {
		t.Fatalf("calls = %d, want >= 2 (opt-in retry of POST)", got)
	}

	bodiesMu.Lock()
	defer bodiesMu.Unlock()

	for i, b := range bodies {
		if b == "" {
			t.Errorf("attempt %d sent empty body; want intact form body", i)
		}
	}
}

// TestRetryMiddleware_596MappedToConnectionError verifies the PVE 596 pseudo
// status is surfaced as a connection error rather than looped on. An idempotent
// GET with no retries should fail immediately with a ConnectionError.
func TestRetryMiddleware_596MappedToConnectionError(t *testing.T) {
	t.Parallel()

	var calls int32

	srv := newTestServer(t, func(writer http.ResponseWriter, _ *http.Request) {
		atomic.AddInt32(&calls, 1)
		writer.WriteHeader(596)
	})

	client := clientPointedAt(t, srv.URL)
	client.maxRetries = 0
	client.retryDelay = time.Millisecond

	_, err := client.Do("GET", "/version", nil)
	if err == nil {
		t.Fatal("expected error for 596")
	}

	var connErr *apierrors.ConnectionError
	if !errorsAs(err, &connErr) {
		t.Errorf("error = %v, want *apierrors.ConnectionError", err)
	}

	if got := atomic.LoadInt32(&calls); got != 1 {
		t.Errorf("calls = %d, want 1 (596 not looped at maxRetries=0)", got)
	}
}

// TestNewClient_TicketOnly_BuildsAuthenticator verifies that providing only a
// pre-existing Ticket (no API token or username) yields a working authenticator
// so requests are authenticated.
func TestNewClient_TicketOnly_BuildsAuthenticator(t *testing.T) {
	t.Parallel()

	opts := minimalHTTPOptions()
	opts.Ticket = "PVE:root@pam:000000::deadbeef"

	client, err := NewClient(opts)
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}

	if client.authenticator == nil {
		t.Fatal("authenticator must not be nil when a ticket is provided")
	}

	headers := client.authenticator.GetHeaders()
	if headers["Cookie"] == "" {
		t.Errorf("expected Cookie header from seeded ticket, got %v", headers)
	}
}

// TestSetTicket_PropagatesToAuthenticator verifies the inner client exposes a
// way to update the ticket on the live authenticator (used by UpdateTicket).
func TestSetTicket_PropagatesToAuthenticator(t *testing.T) {
	t.Parallel()

	opts := minimalHTTPOptions()
	opts.Ticket = testInitialTicket

	client, err := NewClient(opts)
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}

	client.SetTicketValue("PVE:root@pam:000000::updated", "csrf-123")

	ticketAuth, ok := client.authenticator.(*auth.TicketAuthenticator)
	if !ok {
		t.Fatalf("authenticator is %T, want *auth.TicketAuthenticator", client.authenticator)
	}

	tkt := ticketAuth.GetTicket()
	if tkt == nil || tkt.Value != "PVE:root@pam:000000::updated" || tkt.CSRFToken != "csrf-123" {
		t.Errorf("ticket not propagated: %+v", tkt)
	}
}

// TestHandleAuthRetry_APIToken401NotRetried verifies that a 401 carrying a
// static API token is NOT retried. API tokens do not expire and cannot be
// re-issued by the client, so a refresh-and-retry would just replay the same
// rejected token and 401 again. The original 401 must surface after exactly
// one request.
func TestHandleAuthRetry_APIToken401NotRetried(t *testing.T) {
	t.Parallel()

	var calls int32

	srv := newTestServer(t, func(writer http.ResponseWriter, _ *http.Request) {
		atomic.AddInt32(&calls, 1)
		writer.WriteHeader(http.StatusUnauthorized)
		_, _ = writer.Write([]byte(`{"message":"invalid token"}`))
	})

	opts := minimalHTTPOptions()
	opts.APIToken = testAPITokenFull

	client, err := NewClient(opts)
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}

	client.baseURL = srv.URL

	// Precondition: a static-credential authenticator, which cannot re-auth.
	if _, ok := client.authenticator.(*auth.APITokenAuthenticator); !ok {
		t.Fatalf("authenticator is %T, want *auth.APITokenAuthenticator", client.authenticator)
	}

	_, err = client.Do("GET", "/version", nil)
	if err == nil {
		t.Fatal("expected an error from the 401 response")
	}

	if got := atomic.LoadInt32(&calls); got != 1 {
		t.Errorf("calls = %d, want 1 (API-token 401 must not trigger a re-auth retry)", got)
	}
}
