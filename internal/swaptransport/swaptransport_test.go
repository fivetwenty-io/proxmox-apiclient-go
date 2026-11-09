package swaptransport_test

import (
	"context"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/http/httptrace"
	"sync"
	"testing"
	"time"

	"github.com/fivetwenty-io/proxmox-apiclient-go/v3/internal/swaptransport"
)

// newStateServer starts a server that answers "ok" and reports every
// connection state change on the returned channel.
func newStateServer(t *testing.T) (*httptest.Server, <-chan http.ConnState) {
	t.Helper()

	states := make(chan http.ConnState, 64)

	srv := httptest.NewUnstartedServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(writer, "ok")
	}))
	srv.Config.ConnState = func(_ net.Conn, state http.ConnState) { states <- state }
	srv.Start()
	t.Cleanup(srv.Close)

	return srv, states
}

// waitForState drains states until want arrives, failing after a deadline.
func waitForState(t *testing.T, states <-chan http.ConnState, want http.ConnState) {
	t.Helper()

	deadline := time.After(5 * time.Second)

	for {
		select {
		case got := <-states:
			if got == want {
				return
			}
		case <-deadline:
			t.Fatalf("server connection never reached state %v", want)
		}
	}
}

// get sends one GET through client and drains and closes the body.
func get(client *http.Client, url string) error {
	resp, err := client.Get(url) //nolint:noctx // exercising the transport directly
	if err != nil {
		return err //nolint:wrapcheck // test helper hands back the client error
	}

	_, _ = io.Copy(io.Discard, resp.Body)

	return resp.Body.Close() //nolint:wrapcheck // test helper hands back the close error
}

// mustGet is get for the test goroutine, failing the test on error.
func mustGet(t *testing.T, client *http.Client, url string) {
	t.Helper()

	err := get(client, url)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
}

func TestUpdate_InstallsChangedCloneAndRetiresOld(t *testing.T) {
	t.Parallel()

	original := &http.Transport{MaxIdleConns: 3, IdleConnTimeout: 9 * time.Second}
	swap := swaptransport.New(original)

	swap.Update(func(next *http.Transport) bool {
		if next == original {
			t.Error("Update passed the transport in use, want a clone")
		}

		next.MaxIdleConns = 7

		return true
	}).CloseIdleConnections()

	current := swap.Load()

	if current == original || current.MaxIdleConns != 7 || current.IdleConnTimeout != 9*time.Second {
		t.Errorf("current = %p MaxIdleConns %d IdleConnTimeout %v, want a clone with 7 and 9s",
			current, current.MaxIdleConns, current.IdleConnTimeout)
	}

	if original.MaxIdleConns != 3 {
		t.Errorf("the replaced transport changed to MaxIdleConns %d, want 3", original.MaxIdleConns)
	}

	if got := swap.RetiredCount(); got != 1 {
		t.Errorf("RetiredCount = %d, want 1", got)
	}
}

func TestUpdate_BoundsRetiredTransports(t *testing.T) {
	t.Parallel()

	swap := swaptransport.New(&http.Transport{})

	for i := range 20 {
		swap.Update(func(next *http.Transport) bool {
			next.MaxIdleConns = i + 1

			return true
		}).CloseIdleConnections()
	}

	if got := swap.RetiredCount(); got != 8 {
		t.Errorf("RetiredCount = %d after 20 updates, want the bound of 8", got)
	}

	if got := swap.Load().MaxIdleConns; got != 20 {
		t.Errorf("current MaxIdleConns = %d, want 20", got)
	}
}

func TestUpdate_UnchangedKeepsCurrent(t *testing.T) {
	t.Parallel()

	original := &http.Transport{}
	swap := swaptransport.New(original)

	idle := swap.Update(func(*http.Transport) bool { return false })

	if idle != nil || swap.Load() != original || swap.RetiredCount() != 0 {
		t.Error("an Update that changed nothing replaced the transport")
	}
}

func TestUpdate_ClosesReplacedIdleConnectionsAndSendsOnNew(t *testing.T) {
	t.Parallel()

	srv, states := newStateServer(t)
	swap := swaptransport.New(&http.Transport{})
	client := &http.Client{Transport: swap}

	mustGet(t, client, srv.URL)
	waitForState(t, states, http.StateIdle)

	swap.Update(func(next *http.Transport) bool {
		next.MaxIdleConns = 1

		return true
	}).CloseIdleConnections()

	waitForState(t, states, http.StateClosed)

	mustGet(t, client, srv.URL)
	waitForState(t, states, http.StateNew)
	waitForState(t, states, http.StateIdle)

	client.CloseIdleConnections()
	waitForState(t, states, http.StateClosed)
}

func TestUpdate_RaceWithRequests(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(writer, "ok")
	}))
	t.Cleanup(srv.Close)

	swap := swaptransport.New(&http.Transport{MaxIdleConns: 1})
	client := &http.Client{Transport: swap}

	stop := make(chan struct{})
	tuned := make(chan struct{})

	go func() {
		defer close(tuned)

		for i := 0; ; i++ {
			select {
			case <-stop:
				return
			default:
				swap.Update(func(next *http.Transport) bool {
					next.MaxIdleConns = 1 + i%4
					next.MaxIdleConnsPerHost = 1 + i%4

					return true
				}).CloseIdleConnections()
			}
		}
	}()

	var group sync.WaitGroup

	for range 4 {
		group.Go(func() {
			for range 25 {
				err := get(client, srv.URL)
				if err != nil {
					t.Errorf("Get during Update: %v", err)

					return
				}
			}
		})
	}

	group.Wait()
	close(stop)
	<-tuned

	client.CloseIdleConnections()
}

func TestNew_GivesTransportWithoutIdleTimeoutTheDefault(t *testing.T) {
	t.Parallel()

	cases := map[string]struct {
		configured time.Duration
		want       time.Duration
	}{
		"configured": {configured: 7 * time.Second, want: 7 * time.Second},
		"zero":       {configured: 0, want: swaptransport.DefaultIdleConnTimeout},
		"negative":   {configured: -time.Second, want: swaptransport.DefaultIdleConnTimeout},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			swap := swaptransport.New(&http.Transport{IdleConnTimeout: tc.configured})
			if got := swap.Load().IdleConnTimeout; got != tc.want {
				t.Errorf("IdleConnTimeout after New = %v, want %v", got, tc.want)
			}

			swap.Update(func(next *http.Transport) bool {
				next.IdleConnTimeout = 0

				return true
			}).CloseIdleConnections()

			if got := swap.Load().IdleConnTimeout; got != swaptransport.DefaultIdleConnTimeout {
				t.Errorf("IdleConnTimeout after an Update that cleared it = %v, want %v",
					got, swaptransport.DefaultIdleConnTimeout)
			}
		})
	}
}

func TestDefaultIdleConnTimeout_MatchesDefaultTransport(t *testing.T) {
	t.Parallel()

	defaultTransport, ok := http.DefaultTransport.(*http.Transport)
	if !ok {
		t.Skipf("http.DefaultTransport is %T", http.DefaultTransport)
	}

	if defaultTransport.IdleConnTimeout != swaptransport.DefaultIdleConnTimeout {
		t.Errorf("DefaultIdleConnTimeout = %v, net/http uses %v",
			swaptransport.DefaultIdleConnTimeout, defaultTransport.IdleConnTimeout)
	}
}

// pausedGet sends a GET on client whose first GetConn trace pauses, closing
// reached when it pauses and resuming when release closes. The result arrives
// on the returned channel.
func pausedGet(client *http.Client, url string, reached, release chan struct{}) <-chan error {
	var once sync.Once

	trace := &httptrace.ClientTrace{
		GetConn: func(string) {
			once.Do(func() {
				close(reached)
				<-release
			})
		},
	}

	done := make(chan error, 1)

	go func() {
		req, err := http.NewRequestWithContext(
			httptrace.WithClientTrace(context.Background(), trace), http.MethodGet, url, nil)
		if err != nil {
			done <- err

			return
		}

		resp, err := client.Do(req)
		if err != nil {
			done <- err

			return
		}

		_, _ = io.Copy(io.Discard, resp.Body)
		done <- resp.Body.Close()
	}()

	return done
}

// TestUpdate_ConnectionOfRequestPausedAcrossEvictionTimesOut pauses a request
// in GetConn, after it loaded the original transport but before it asked that
// transport for a connection, while more than maxRetired updates evict the
// original from the retired list. Resuming resets the original's close-idle
// state, so the connection it releases is pooled where no
// CloseIdleConnections call can reach it. The test shows the connection is
// still closed, by the finite IdleConnTimeout.
func TestUpdate_ConnectionOfRequestPausedAcrossEvictionTimesOut(t *testing.T) {
	t.Parallel()

	const idleTimeout = 100 * time.Millisecond

	srv, states := newStateServer(t)
	swap := swaptransport.New(&http.Transport{IdleConnTimeout: idleTimeout})
	client := &http.Client{Transport: swap}

	reached := make(chan struct{})
	release := make(chan struct{})
	done := pausedGet(client, srv.URL, reached, release)

	select {
	case <-reached:
	case <-time.After(5 * time.Second):
		close(release)
		t.Fatal("the request never reached GetConn")
	}

	for i := range 10 {
		swap.Update(func(next *http.Transport) bool {
			next.MaxIdleConns = i + 1

			return true
		}).CloseIdleConnections()
	}

	if got := swap.RetiredCount(); got != 8 {
		t.Fatalf("RetiredCount = %d, want 8 with the original evicted", got)
	}

	close(release)

	err := <-done
	if err != nil {
		t.Fatalf("paused request: %v", err)
	}

	waitForState(t, states, http.StateIdle)

	// Closing every transport the swap still reaches cannot touch the
	// evicted original; only its idle timer can close the connection.
	client.CloseIdleConnections()

	idleAt := time.Now()

	waitForState(t, states, http.StateClosed)

	if elapsed := time.Since(idleAt); elapsed > 5*time.Second {
		t.Errorf("connection closed after %v, want about the %v idle timeout", elapsed, idleTimeout)
	}
}
