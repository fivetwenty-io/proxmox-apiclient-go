package client_test

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	pve "github.com/fivetwenty-io/proxmox-apiclient-go/v3/pkg/client"
)

var errRefused = errors.New("wrapper refused")

// redirectServers starts a target listener and a front listener that answers
// every request with a 307 to the target, which is on another port of the same
// host. The counters hold what each one read.
func redirectServers(t *testing.T) (*httptest.Server, *atomic.Int32, *atomic.Int32) {
	t.Helper()

	frontHits, targetHits := new(atomic.Int32), new(atomic.Int32)

	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		targetHits.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"data":{"version":"test"}}`)
	}))
	t.Cleanup(target.Close)

	front := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		frontHits.Add(1)
		http.Redirect(w, r, target.URL+r.URL.Path, http.StatusTemporaryRedirect)
	}))
	t.Cleanup(front.Close)

	return front, frontHits, targetHits
}

//nolint:ireturn // test helper returning the public client interface
func newRedirectClient(t *testing.T, front *httptest.Server, mutate func(*pve.Options)) pve.Client {
	t.Helper()

	host, port := parseHostPort(front.URL)
	opts := pve.Options{Host: host, Port: port, Protocol: testProtoHTTP, APIToken: testAPIToken}

	if mutate != nil {
		mutate(&opts)
	}

	cli, err := pve.NewClient(opts)
	if err != nil {
		t.Fatalf("NewClient() error = %v", err)
	}

	t.Cleanup(func() { _ = cli.Close() })

	return cli
}

func retryingContext() context.Context {
	return pve.WithRetryDelay(pve.WithRetries(context.Background(), 3), time.Millisecond)
}

// TestRedirect_RefusedByDefault checks the public client: the redirect is
// refused with the exported error, the target reads nothing, and the front
// listener reads one request although the context allows three retries.
func TestRedirect_RefusedByDefault(t *testing.T) {
	t.Parallel()

	front, frontHits, targetHits := redirectServers(t)
	cli := newRedirectClient(t, front, nil)

	_, err := cli.GetCtx(retryingContext(), "/version", nil)
	if !errors.Is(err, pve.ErrRedirectRefused) {
		t.Fatalf("error = %v, want pve.ErrRedirectRefused", err)
	}

	var refusal *pve.RedirectError
	if !errors.As(err, &refusal) || refusal.Status != http.StatusTemporaryRedirect {
		t.Errorf("error = %v, want a *pve.RedirectError for 307", err)
	}

	if !pve.IsPermanent(err) {
		t.Error("the refusal must carry the permanent marker")
	}

	if got := targetHits.Load(); got != 0 {
		t.Errorf("target read %d requests, want 0", got)
	}

	if got := frontHits.Load(); got != 1 {
		t.Errorf("front read %d requests, want 1", got)
	}
}

// TestRedirect_OptInFollows checks that Options.CheckRedirect restores
// following.
func TestRedirect_OptInFollows(t *testing.T) {
	t.Parallel()

	front, _, targetHits := redirectServers(t)
	cli := newRedirectClient(t, front, func(o *pve.Options) {
		o.CheckRedirect = func(*http.Request, []*http.Request) error { return nil }
	})

	_, err := cli.GetCtx(retryingContext(), "/version", nil)
	if err != nil {
		t.Fatalf("GetCtx with the opt-in failed: %v", err)
	}

	if got := targetHits.Load(); got != 1 {
		t.Errorf("target read %d requests, want 1", got)
	}
}

// TestRedirect_WrapTransportRefusalIsAttemptedOnce checks the exported marker
// from a wrapper's point of view.
func TestRedirect_WrapTransportRefusalIsAttemptedOnce(t *testing.T) {
	t.Parallel()

	front, _, _ := redirectServers(t)

	var permanentCalls, plainCalls atomic.Int32

	for _, tc := range []struct {
		name  string
		calls *atomic.Int32
		fail  error
		want  int32
	}{
		{"permanent", &permanentCalls, pve.Permanent(errRefused), 1},
		{"plain", &plainCalls, errRefused, 4},
	} {
		cli := newRedirectClient(t, front, func(o *pve.Options) {
			o.WrapTransport = func(*http.Transport) http.RoundTripper {
				return roundTripFunc(func(*http.Request) (*http.Response, error) {
					tc.calls.Add(1)

					return nil, tc.fail
				})
			}
		})

		_, err := cli.GetCtx(retryingContext(), "/version", nil)
		if !errors.Is(err, errRefused) {
			t.Fatalf("%s: error = %v, want the wrapper's error", tc.name, err)
		}

		if got := tc.calls.Load(); got != tc.want {
			t.Errorf("%s: attempts = %d, want %d", tc.name, got, tc.want)
		}
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) { return f(req) }

// TestRedirect_FollowRedirectsStopsAtTheLimit sends the client to a listener
// that redirects to itself, which a policy returning nil would follow without
// end.
func TestRedirect_FollowRedirectsStopsAtTheLimit(t *testing.T) {
	t.Parallel()

	var hits atomic.Int32

	loop := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		http.Redirect(w, r, r.URL.Path, http.StatusFound)
	}))
	t.Cleanup(loop.Close)

	cli := newRedirectClient(t, loop, func(o *pve.Options) { o.CheckRedirect = pve.FollowRedirects(3) })

	_, err := cli.GetCtx(retryingContext(), "/version", nil)
	if !errors.Is(err, pve.ErrRedirectRefused) {
		t.Fatalf("error = %v, want pve.ErrRedirectRefused", err)
	}

	if got := hits.Load(); got != 4 {
		t.Errorf("listener read %d requests, want the first plus 3 redirects", got)
	}
}

// TestRedirect_FollowSameOrigin follows a redirect within the origin and
// refuses one to another port.
func TestRedirect_FollowSameOrigin(t *testing.T) {
	t.Parallel()

	t.Run("same origin followed", func(t *testing.T) {
		t.Parallel()

		var hits atomic.Int32

		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if hits.Add(1) == 1 {
				http.Redirect(w, r, "/api2/json/moved", http.StatusFound)

				return
			}

			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, `{"data":{"version":"test"}}`)
		}))
		t.Cleanup(srv.Close)

		cli := newRedirectClient(t, srv, func(o *pve.Options) { o.CheckRedirect = pve.FollowSameOrigin(2) })

		_, err := cli.GetCtx(retryingContext(), "/version", nil)
		if err != nil {
			t.Fatalf("same-origin redirect failed: %v", err)
		}
	})

	t.Run("other port refused", func(t *testing.T) {
		t.Parallel()

		front, frontHits, targetHits := redirectServers(t)
		cli := newRedirectClient(t, front, func(o *pve.Options) { o.CheckRedirect = pve.FollowSameOrigin(2) })

		_, err := cli.GetCtx(retryingContext(), "/version", nil)
		if !errors.Is(err, pve.ErrRedirectRefused) {
			t.Fatalf("error = %v, want pve.ErrRedirectRefused", err)
		}

		if targetHits.Load() != 0 || frontHits.Load() != 1 {
			t.Errorf("target read %d, front read %d, want 0 and 1", targetHits.Load(), frontHits.Load())
		}
	})
}
