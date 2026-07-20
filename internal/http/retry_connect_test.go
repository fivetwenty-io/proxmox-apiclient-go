package http //nolint:testpackage // white-box: exercises retryMiddleware directly

import (
	"context"
	"net"
	"net/http"
	"sync/atomic"
	"testing"
	"time"

	apierrors "github.com/fivetwenty-io/proxmox-apiclient-go/v3/pkg/errors"
)

// countingClient points a client at addr and rebuilds its middleware chain as
// retryMiddleware wrapping a counter, so tests can assert the exact number of
// transport attempts a request made.
//
// Order matters: executeWithMiddleware wraps in reverse, so middleware[0] is the
// OUTERMOST layer. The counter must sit AFTER retryMiddleware to be re-entered
// on every attempt; placing it first counts the whole retry loop as one call.
func countingClient(t *testing.T, addr string, calls *int32) *Client {
	t.Helper()

	client := clientPointedAt(t, addr)
	client.maxRetries = 3
	client.retryDelay = time.Millisecond
	client.middleware = []Middleware{
		client.retryMiddleware,
		func(r *http.Request, next Handler) (*http.Response, error) {
			atomic.AddInt32(calls, 1)

			return next(r)
		},
	}

	return client
}

// TestRetryMiddleware_DialRefusedNotRetried verifies that a refused TCP dial is
// terminal even for an idempotent method. Retrying cannot help when the
// connection was never established, and each attempt costs a full dial timeout.
func TestRetryMiddleware_DialRefusedNotRetried(t *testing.T) {
	t.Parallel()

	var calls int32

	client := countingClient(t, "http://127.0.0.1:1", &calls) // nothing listens on port 1

	_, err := client.Do("GET", "/version", nil)
	if err == nil {
		t.Fatal("expected an error dialing a closed port")
	}

	if got := atomic.LoadInt32(&calls); got != 1 {
		t.Errorf("calls = %d, want 1 (a refused dial must not be retried)", got)
	}

	var connErr *apierrors.ConnectionError
	if !errorsAs(err, &connErr) {
		t.Errorf("error = %v, want a *apierrors.ConnectionError", err)
	}
}

// TestRetryMiddleware_DNSFailureNotRetried verifies that an unresolvable host is
// terminal. Repeating the lookup three more times cannot make the name resolve.
func TestRetryMiddleware_DNSFailureNotRetried(t *testing.T) {
	t.Parallel()

	var calls int32

	client := countingClient(t, "http://pmx-nonexistent.invalid:8006", &calls)

	_, err := client.Do("GET", "/version", nil)
	if err == nil {
		t.Fatal("expected an error resolving an invalid host")
	}

	if got := atomic.LoadInt32(&calls); got != 1 {
		t.Errorf("calls = %d, want 1 (a DNS failure must not be retried)", got)
	}

	var connErr *apierrors.ConnectionError
	if !errorsAs(err, &connErr) {
		t.Errorf("error = %v, want a *apierrors.ConnectionError", err)
	}
}

// TestRetryMiddleware_PostConnectErrorStillRetried is the regression guard for
// over-broad classification: a transport error that happens AFTER the connection
// was established (here the server hangs up mid-response) is genuinely transient
// and must still be retried.
func TestRetryMiddleware_PostConnectErrorStillRetried(t *testing.T) {
	t.Parallel()

	var calls int32

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

		_ = conn.Close() // hang up without writing a response
	})

	client := countingClient(t, srv.URL, &calls)

	_, err := client.Do("GET", "/version", nil)
	if err == nil {
		t.Fatal("expected an error when the server hangs up")
	}

	if got := atomic.LoadInt32(&calls); got != 4 {
		t.Errorf("calls = %d, want 4 (a post-connect failure must still be retried)", got)
	}
}

// TestRetryMiddleware_BackoffHonorsCancellation verifies that canceling the
// request context interrupts the retry backoff instead of sleeping it out. The
// backoff used to be a bare time.Sleep, so Ctrl-C could not shorten a retry
// storm against an unhealthy server.
func TestRetryMiddleware_BackoffHonorsCancellation(t *testing.T) {
	t.Parallel()

	srv := newTestServer(t, func(writer http.ResponseWriter, _ *http.Request) {
		writer.WriteHeader(http.StatusServiceUnavailable)
		_, _ = writer.Write([]byte(`{"message":"unavailable"}`))
	})

	client := clientPointedAt(t, srv.URL)
	client.maxRetries = 3
	client.retryDelay = time.Hour // would hang effectively forever if not cancellable

	ctx, cancel := context.WithCancel(context.Background())

	go func() {
		time.Sleep(50 * time.Millisecond)
		cancel()
	}()

	done := make(chan error, 1)

	go func() {
		_, err := client.DoWithContext(ctx, "GET", "/version", nil)
		done <- err
	}()

	select {
	case err := <-done:
		if err == nil {
			t.Fatal("expected an error after cancellation")
		}
	case <-time.After(10 * time.Second):
		t.Fatal("retry backoff ignored context cancellation")
	}
}

// TestIsTerminalTransportError covers the classifier directly, including the
// negative cases that keep transient failures retryable.
func TestIsTerminalTransportError(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		err  error
		want bool
	}{
		{"dial refused", &net.OpError{Op: "dial", Err: connRefusedError{}}, true},
		{"dns failure", &net.DNSError{Err: "no such host", Name: "nope.invalid"}, true},
		{"read reset", &net.OpError{Op: "read", Err: connRefusedError{}}, false},
		{"write reset", &net.OpError{Op: "write", Err: connRefusedError{}}, false},
		{"nil", nil, false},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			if got := isTerminalTransportError(tc.err); got != tc.want {
				t.Errorf("isTerminalTransportError(%v) = %v, want %v", tc.err, got, tc.want)
			}
		})
	}
}

// connRefusedError is a stand-in for a syscall-level connection error.
type connRefusedError struct{}

func (connRefusedError) Error() string { return "connection refused" }
