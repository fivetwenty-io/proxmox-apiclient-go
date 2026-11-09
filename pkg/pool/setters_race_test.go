package pool_test

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/fivetwenty-io/proxmox-apiclient-go/v3/pkg/pool"
)

// sharedClient returns the pool's shared client and its *http.Transport,
// failing the test if the Transport is anything else.
func sharedClient(t *testing.T, poolInstance *pool.Pool) (*http.Client, *http.Transport) {
	t.Helper()

	client, err := poolInstance.Get()
	if err != nil {
		t.Fatalf("Get: %v", err)
	}

	poolInstance.Put(client)

	transport, ok := client.Transport.(*http.Transport)
	if !ok {
		t.Fatalf("Transport is %T, want *http.Transport", client.Transport)
	}

	return client, transport
}

// TestPoolSetters_InstallNewClient verifies the setters make a new client and
// transport carrying the new limits the shared client, leave the replaced pair
// untouched, and skip a call that changes nothing.
func TestPoolSetters_InstallNewClient(t *testing.T) {
	t.Parallel()

	poolInstance := pool.New(pool.DefaultConfig())

	t.Cleanup(func() { _ = poolInstance.Close() })

	original, originalTransport := sharedClient(t, poolInstance)
	originalIdle, originalPerHost := originalTransport.MaxIdleConns, originalTransport.MaxIdleConnsPerHost

	poolInstance.SetMaxConnections(originalIdle + 7)
	poolInstance.SetMaxConnectionsPerHost(originalPerHost + 3)

	current, currentTransport := sharedClient(t, poolInstance)

	if current == original || currentTransport == originalTransport {
		t.Fatal("the setters did not install a new client and transport")
	}

	if currentTransport.MaxIdleConns != originalIdle+7 || currentTransport.MaxIdleConnsPerHost != originalPerHost+3 {
		t.Errorf("current idle limits = %d/%d, want %d/%d",
			currentTransport.MaxIdleConns, currentTransport.MaxIdleConnsPerHost, originalIdle+7, originalPerHost+3)
	}

	if originalTransport.MaxIdleConns != originalIdle || originalTransport.MaxIdleConnsPerHost != originalPerHost {
		t.Error("a setter wrote the transport that was in use")
	}

	if original.Transport != originalTransport {
		t.Error("a setter changed the Transport of a client already handed out")
	}

	poolInstance.SetMaxConnections(originalIdle + 7)
	poolInstance.SetMaxConnectionsPerHost(originalPerHost + 3)

	if again, _ := sharedClient(t, poolInstance); again != current {
		t.Error("a setter that changed nothing replaced the shared client")
	}
}

// TestPool_TransportsHaveFiniteIdleTimeout verifies every transport the pool
// builds, at New and after a setter, has an IdleConnTimeout, falling back to
// 90 seconds when Config.IdleTimeout is zero.
func TestPool_TransportsHaveFiniteIdleTimeout(t *testing.T) {
	t.Parallel()

	cases := map[string]struct {
		configured time.Duration
		want       time.Duration
	}{
		"configured": {configured: 7 * time.Second, want: 7 * time.Second},
		"zero":       {configured: 0, want: 90 * time.Second},
		"negative":   {configured: -time.Second, want: 90 * time.Second},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			config := pool.DefaultConfig()
			config.IdleTimeout = tc.configured

			poolInstance := pool.New(config)

			t.Cleanup(func() { _ = poolInstance.Close() })

			_, initial := sharedClient(t, poolInstance)
			poolInstance.SetMaxConnections(config.MaxConnections + 1)
			_, retuned := sharedClient(t, poolInstance)

			if initial.IdleConnTimeout != tc.want || retuned.IdleConnTimeout != tc.want {
				t.Errorf("IdleConnTimeout initial/retuned = %v/%v, want %v",
					initial.IdleConnTimeout, retuned.IdleConnTimeout, tc.want)
			}
		})
	}
}

// TestPoolSetters_ClosedPoolRecordsOnly verifies a setter on a closed pool
// builds no client.
func TestPoolSetters_ClosedPoolRecordsOnly(t *testing.T) {
	t.Parallel()

	poolInstance := pool.New(pool.DefaultConfig())

	err := poolInstance.Close()
	if err != nil {
		t.Fatalf("Close: %v", err)
	}

	poolInstance.SetMaxConnections(1)
	poolInstance.SetMaxConnectionsPerHost(1)

	_, err = poolInstance.Get()
	if err == nil {
		t.Fatal("Get on a closed pool succeeded")
	}
}

// TestPoolSetters_RaceWithRequests calls both setters while requests run and
// their response bodies close, so go test -race catches a write to the
// transport or client in use.
func TestPoolSetters_RaceWithRequests(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(writer, "ok")
	}))
	t.Cleanup(srv.Close)

	poolInstance := pool.New(pool.DefaultConfig())

	t.Cleanup(func() { _ = poolInstance.Close() })

	stop := make(chan struct{})
	tuned := make(chan struct{})

	go func() {
		defer close(tuned)

		for i := 0; ; i++ {
			select {
			case <-stop:
				return
			default:
				poolInstance.SetMaxConnections(1 + i%4)
				poolInstance.SetMaxConnectionsPerHost(1 + i%3)
			}
		}
	}()

	var group sync.WaitGroup

	for range 4 {
		group.Go(func() {
			for range 25 {
				if !sendThroughPool(t, poolInstance, srv.URL) {
					return
				}
			}
		})
	}

	group.Wait()
	close(stop)
	<-tuned
}

// sendThroughPool sends one GET through the pool and drains and closes the
// body, reporting failures with t.Errorf so it is safe off the test goroutine.
func sendThroughPool(t *testing.T, poolInstance *pool.Pool, url string) bool {
	t.Helper()

	req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, url, nil)
	if err != nil {
		t.Errorf("NewRequest: %v", err)

		return false
	}

	resp, err := poolInstance.Do(req)
	if err != nil {
		t.Errorf("Do during the setters: %v", err)

		return false
	}

	_, _ = io.Copy(io.Discard, resp.Body)
	_ = resp.Body.Close()

	return true
}
