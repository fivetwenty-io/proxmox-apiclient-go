package http //nolint:testpackage // white-box test: counts attempts through the client's middleware chain

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

var (
	errPolicy  = errors.New("policy says no")
	errRefused = errors.New("wrapper refused the request")
	errBase    = errors.New("base")
	errReset   = errors.New("connection reset by peer")
)

const (
	redirectTestPath  = "/api2/json/version"
	redirectTestRetry = 3
)

// redirectRig is a token-authenticated client whose first listener answers
// every request with a redirect to a second plain listener on another port of
// the same host, which is the redirect Go's default policy follows with the
// Authorization header attached.
type redirectRig struct {
	client *Client

	// attempts counts the requests that reached the transport layer of the
	// retry middleware, one per attempt.
	attempts atomic.Int32

	// firstHits and targetHits count the requests each listener read.
	firstHits  atomic.Int32
	targetHits atomic.Int32

	// targetAuth records whether any request at the target carried the token.
	targetAuth atomic.Bool
}

func newRedirectRig(t *testing.T, status int, mutate func(*Options)) *redirectRig {
	t.Helper()

	rig := &redirectRig{}

	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rig.targetHits.Add(1)

		if r.Header.Get("Authorization") != "" {
			rig.targetAuth.Store(true)
		}

		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":"followed"}`))
	}))
	t.Cleanup(target.Close)

	first := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rig.firstHits.Add(1)
		http.Redirect(w, r, target.URL+redirectTestPath, status)
	}))
	t.Cleanup(first.Close)

	opts := minimalHTTPOptions()
	opts.APIToken = testAPITokenFull

	if mutate != nil {
		mutate(opts)
	}

	client, err := NewClient(opts)
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}

	client.baseURL = first.URL
	client.SetMaxRetries(redirectTestRetry)
	client.SetRetryDelay(time.Millisecond)
	client.middleware = []Middleware{
		client.retryMiddleware,
		func(r *http.Request, next Handler) (*http.Response, error) {
			rig.attempts.Add(1)

			return next(r)
		},
	}
	rig.client = client

	return rig
}

func (rig *redirectRig) do(method string) error {
	ctx := context.Background()

	if method != http.MethodGet {
		// Without the opt-in a POST is never retried, so the opt-in makes the
		// refusal the only thing that can stop a second attempt.
		ctx = WithForceRetry(ctx, true)
	}

	_, err := rig.client.DoWithContext(ctx, method, "/version", nil)

	return err
}

// TestRedirectRefusedByDefault sends a GET and a POST to a listener that
// answers 302 and 307 with a redirect to another port of the same host. The
// target must read nothing, so the token never arrives, and the caller gets
// the refusal after exactly one attempt.
func TestRedirectRefusedByDefault(t *testing.T) {
	t.Parallel()

	for _, method := range []string{http.MethodGet, http.MethodPost} {
		for _, status := range []int{http.StatusFound, http.StatusTemporaryRedirect} {
			t.Run(fmt.Sprintf("%s_%d", method, status), func(t *testing.T) {
				t.Parallel()

				rig := newRedirectRig(t, status, nil)

				rig.assertRefused(t, rig.do(method), status)
			})
		}
	}
}

// assertRefused checks everything a refused redirect must leave behind.
func (rig *redirectRig) assertRefused(t *testing.T, err error, status int) {
	t.Helper()

	if !errors.Is(err, ErrRedirectRefused) {
		t.Fatalf("error = %v, want ErrRedirectRefused", err)
	}

	var refusal *RedirectError
	if !errors.As(err, &refusal) {
		t.Fatalf("error = %v, want *RedirectError", err)
	}

	if refusal.Status != status {
		t.Errorf("refusal status = %d, want %d", refusal.Status, status)
	}

	if got := rig.targetHits.Load(); got != 0 {
		t.Errorf("target read %d requests, want 0", got)
	}

	if rig.targetAuth.Load() {
		t.Error("the token reached the redirect target")
	}

	if got := rig.attempts.Load(); got != 1 {
		t.Errorf("attempts = %d, want 1", got)
	}

	if got := rig.firstHits.Load(); got != 1 {
		t.Errorf("first listener read %d requests, want 1", got)
	}

	msg := err.Error()
	if !strings.Contains(msg, refusal.To) || strings.Contains(msg, "s3cr3t") {
		t.Errorf("message %q must name the target %q and carry no secret", msg, refusal.To)
	}
}

// TestRedirectRefusalNamesNoCredentials checks that user information and a
// query in the redirect target stay out of the error.
func TestRedirectRefusalNamesNoCredentials(t *testing.T) {
	t.Parallel()

	req, err := http.NewRequestWithContext(
		context.Background(), http.MethodGet, "http://user:hunter2@10.0.0.1:8080/x?ticket=abc", nil,
	)
	if err != nil {
		t.Fatalf("NewRequest: %v", err)
	}

	req.Response = &http.Response{StatusCode: http.StatusFound}

	via, err := http.NewRequestWithContext(context.Background(), http.MethodGet, "https://10.0.0.1:8006/a?b=c", nil)
	if err != nil {
		t.Fatalf("NewRequest: %v", err)
	}

	refusal := refuseRedirect(req, []*http.Request{via})
	if !errors.Is(refusal, ErrRedirectRefused) || !IsPermanent(refusal) {
		t.Fatalf("refusal = %v, want a permanent ErrRedirectRefused", refusal)
	}

	want := "redirect refused: https://10.0.0.1:8006/a answered 302 and asked for http://10.0.0.1:8080/x"
	if got := refusal.Error(); got != want {
		t.Errorf("message = %q, want %q", got, want)
	}
}

// TestRedirectOptInFollows verifies that a caller's CheckRedirect that allows
// the redirect gets the old behavior, and that a caller's own refusal wins.
func TestRedirectOptInFollows(t *testing.T) {
	t.Parallel()

	t.Run("follows", func(t *testing.T) {
		t.Parallel()

		rig := newRedirectRig(t, http.StatusTemporaryRedirect, func(o *Options) {
			o.CheckRedirect = func(*http.Request, []*http.Request) error { return nil }
		})

		err := rig.do(http.MethodGet)
		if err != nil {
			t.Fatalf("request with the opt-in failed: %v", err)
		}

		if got := rig.targetHits.Load(); got != 1 {
			t.Errorf("target read %d requests, want 1", got)
		}
	})

	t.Run("own policy error", func(t *testing.T) {
		t.Parallel()

		rig := newRedirectRig(t, http.StatusFound, func(o *Options) {
			o.CheckRedirect = func(*http.Request, []*http.Request) error { return Permanent(errPolicy) }
		})

		err := rig.do(http.MethodGet)
		if !errors.Is(err, errPolicy) || errors.Is(err, ErrRedirectRefused) {
			t.Fatalf("error = %v, want the caller's policy error", err)
		}

		if got := rig.attempts.Load(); got != 1 {
			t.Errorf("attempts = %d, want 1", got)
		}

		if got := rig.targetHits.Load(); got != 0 {
			t.Errorf("target read %d requests, want 0", got)
		}
	})
}

// countingTransport is a WrapTransport wrapper that fails every request with
// err, and counts the requests.
type countingTransport struct {
	err   error
	count atomic.Int32
}

func (c *countingTransport) RoundTrip(*http.Request) (*http.Response, error) {
	c.count.Add(1)

	return nil, c.err
}

func clientWithFailingTransport(t *testing.T, failure error) (*Client, *countingTransport) {
	t.Helper()

	wrapper := &countingTransport{err: failure}
	opts := minimalHTTPOptions()
	opts.APIToken = testAPITokenFull
	opts.WrapTransport = func(*http.Transport) http.RoundTripper { return wrapper }

	client, err := NewClient(opts)
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}

	client.SetMaxRetries(redirectTestRetry)
	client.SetRetryDelay(time.Millisecond)

	return client, wrapper
}

// TestPermanentTransportErrorIsAttemptedOnce verifies that a wrapper can mark
// its own refusal as permanent, however the error is wrapped on the way out.
func TestPermanentTransportErrorIsAttemptedOnce(t *testing.T) {
	t.Parallel()

	cases := map[string]error{
		"helper":         Permanent(errRefused),
		"wrapped helper": fmt.Errorf("context: %w", Permanent(errRefused)),
		"own marker":     &markedError{},
	}

	for name, failure := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			client, wrapper := clientWithFailingTransport(t, failure)

			_, err := client.Do(http.MethodGet, "/version", nil)
			if !IsPermanent(err) {
				t.Fatalf("error = %v, want the wrapper's permanent error", err)
			}

			if got := wrapper.count.Load(); got != 1 {
				t.Errorf("attempts = %d, want 1", got)
			}
		})
	}
}

// markedError is an error type that carries the marker itself.
type markedError struct{}

func (*markedError) Error() string { return "own marker" }

func (*markedError) Permanent() bool { return true }

// TestPermanentKeepsTheChain verifies the helper leaves errors.Is and errors.As
// working and leaves a nil error nil.
func TestPermanentKeepsTheChain(t *testing.T) {
	t.Parallel()

	base := errBase
	marked := Permanent(base)

	if !errors.Is(marked, base) || !IsPermanent(marked) || marked.Error() != base.Error() {
		t.Errorf("Permanent(%v) = %v, want a permanent error that wraps and reads as the original", base, marked)
	}

	if Permanent(nil) != nil || IsPermanent(nil) || IsPermanent(base) {
		t.Error("nil stays nil, and an unmarked error is not permanent")
	}
}

// TestOrdinaryTransportErrorsStillRetry guards the other side: an error with no
// marker is attempted once plus the retry count, and a marker that says false
// does not stop the loop.
func TestOrdinaryTransportErrorsStillRetry(t *testing.T) {
	t.Parallel()

	cases := map[string]error{
		"plain":        errReset,
		"marker false": &nonPermanentError{},
	}

	for name, failure := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			client, wrapper := clientWithFailingTransport(t, failure)

			_, err := client.Do(http.MethodGet, "/version", nil)
			if err == nil {
				t.Fatal("expected an error")
			}

			if got, want := wrapper.count.Load(), int32(redirectTestRetry+1); got != want {
				t.Errorf("attempts = %d, want %d", got, want)
			}
		})
	}
}

type nonPermanentError struct{}

func (*nonPermanentError) Error() string { return "marker false" }

func (*nonPermanentError) Permanent() bool { return false }
