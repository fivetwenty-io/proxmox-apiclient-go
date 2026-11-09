package pool_test

import (
	"net/http"
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

// TestPoolNew_UsesConfiguredIdleTimeout verifies the pool's transport takes
// Config.IdleTimeout as it is, so zero keeps idle connections without a time
// limit, as net/http defines for IdleConnTimeout.
func TestPoolNew_UsesConfiguredIdleTimeout(t *testing.T) {
	t.Parallel()

	for name, timeout := range map[string]time.Duration{"configured": 7 * time.Second, "zero": 0} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			config := pool.DefaultConfig()
			config.IdleTimeout = timeout

			poolInstance := pool.New(config)

			t.Cleanup(func() { _ = poolInstance.Close() })

			_, transport := sharedClient(t, poolInstance)
			if transport.IdleConnTimeout != timeout {
				t.Errorf("IdleConnTimeout = %v, want %v", transport.IdleConnTimeout, timeout)
			}
		})
	}
}

// TestPoolSetters_AfterClose verifies the setters on a closed pool return
// without panicking and leave the pool closed.
func TestPoolSetters_AfterClose(t *testing.T) {
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

// TestPoolSetters_ConcurrentWithPoolMethods runs both setters from several
// goroutines alongside Get, Put, Stats, IsHealthy, and finally Close, with no
// request in flight, so go test -race catches an unguarded write to the
// pool's configuration or transport.
func TestPoolSetters_ConcurrentWithPoolMethods(t *testing.T) {
	t.Parallel()

	poolInstance := pool.New(pool.DefaultConfig())

	var group sync.WaitGroup

	for i := range 4 {
		group.Go(func() {
			for j := range 200 {
				poolInstance.SetMaxConnections(1 + (i+j)%7)
				poolInstance.SetMaxConnectionsPerHost(1 + (i+j)%5)
			}
		})
	}

	group.Go(func() {
		for range 200 {
			client, err := poolInstance.Get()
			if err != nil {
				return
			}

			poolInstance.Put(client)
		}
	})

	group.Go(func() {
		for range 200 {
			_ = poolInstance.Stats()
			_ = poolInstance.IsHealthy()
		}
	})

	group.Go(func() {
		time.Sleep(time.Millisecond)

		_ = poolInstance.Close()
	})

	group.Wait()

	if poolInstance.IsHealthy() {
		t.Error("the pool reports healthy after Close")
	}
}

// TestPoolNew_CopiesConfig verifies two pools built from one *Config share
// nothing: setters on both run concurrently without a race, and neither
// writes the caller's Config.
func TestPoolNew_CopiesConfig(t *testing.T) {
	t.Parallel()

	config := pool.DefaultConfig()
	wantMax, wantPerHost := config.MaxConnections, config.MaxConnectionsPerHost

	first := pool.New(config)
	second := pool.New(config)

	t.Cleanup(func() {
		_ = first.Close()
		_ = second.Close()
	})

	var group sync.WaitGroup

	for i := range 4 {
		group.Go(func() {
			for j := range 50 {
				first.SetMaxConnections(1 + (i+j)%7)
				second.SetMaxConnectionsPerHost(1 + (i+j)%5)
				first.SetMaxConnectionsPerHost(1 + (i+j)%3)
				second.SetMaxConnections(1 + (i+j)%6)
			}
		})
	}

	group.Wait()

	if config.MaxConnections != wantMax || config.MaxConnectionsPerHost != wantPerHost {
		t.Errorf("caller Config = %d/%d after the setters, want it unchanged at %d/%d",
			config.MaxConnections, config.MaxConnectionsPerHost, wantMax, wantPerHost)
	}
}
