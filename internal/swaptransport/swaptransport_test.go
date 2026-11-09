package swaptransport_test

import (
	"io"
	"net"
	"net/http"
	"net/http/httptest"
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
	})

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
		})
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

	swap.Update(func(*http.Transport) bool { return false })

	if swap.Load() != original || swap.RetiredCount() != 0 {
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
	})

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
				})
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
