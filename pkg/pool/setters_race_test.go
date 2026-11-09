package pool_test

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/fivetwenty-io/proxmox-apiclient-go/v3/internal/swaptransport"
	"github.com/fivetwenty-io/proxmox-apiclient-go/v3/pkg/pool"
)

// sharedTransport returns the swappable transport behind the pool's shared
// client.
func sharedTransport(t *testing.T, poolInstance *pool.Pool) (*http.Client, *swaptransport.Transport) {
	t.Helper()

	client, err := poolInstance.Get()
	if err != nil {
		t.Fatalf("Get: %v", err)
	}

	poolInstance.Put(client)

	swap, ok := client.Transport.(*swaptransport.Transport)
	if !ok {
		t.Fatalf("Transport is %T, want *swaptransport.Transport", client.Transport)
	}

	return client, swap
}

// TestPoolSetters_InstallRetunedTransport verifies the setters reach the
// transport later requests use, leave the replaced one untouched, keep the
// same shared client, and skip a call that changes nothing.
func TestPoolSetters_InstallRetunedTransport(t *testing.T) {
	t.Parallel()

	poolInstance := pool.New(pool.DefaultConfig())

	t.Cleanup(func() { _ = poolInstance.Close() })

	client, swap := sharedTransport(t, poolInstance)
	original := swap.Load()
	originalIdle, originalPerHost := original.MaxIdleConns, original.MaxIdleConnsPerHost

	poolInstance.SetMaxConnections(originalIdle + 7)
	poolInstance.SetMaxConnectionsPerHost(originalPerHost + 3)

	current := swap.Load()
	if current.MaxIdleConns != originalIdle+7 || current.MaxIdleConnsPerHost != originalPerHost+3 {
		t.Errorf("current idle limits = %d/%d, want %d/%d",
			current.MaxIdleConns, current.MaxIdleConnsPerHost, originalIdle+7, originalPerHost+3)
	}

	if original.MaxIdleConns != originalIdle || original.MaxIdleConnsPerHost != originalPerHost {
		t.Error("a setter wrote the transport that was in use")
	}

	if again, _ := sharedTransport(t, poolInstance); again != client {
		t.Error("Get returned a different *http.Client after the setters ran")
	}

	retired := swap.RetiredCount()

	poolInstance.SetMaxConnections(originalIdle + 7)
	poolInstance.SetMaxConnectionsPerHost(originalPerHost + 3)

	if swap.RetiredCount() != retired {
		t.Error("a setter that changed nothing replaced the transport")
	}
}

// TestPoolSetters_RaceWithRequests calls both setters while requests run and
// their response bodies close, so go test -race catches a write to the
// transport in use.
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
