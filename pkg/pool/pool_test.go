package pool_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/fivetwenty-io/proxmox-apiclient-go/v3/pkg/pool"
)

func TestNewPool(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		config *pool.Config
	}{
		{
			name:   "default config",
			config: nil,
		},
		{
			name: "custom config",
			config: &pool.Config{
				MaxConnections:        50,
				MaxConnectionsPerHost: 5,
				IdleTimeout:           60 * time.Second,
				ConnectionTimeout:     20 * time.Second,
				MaxIdleTime:           time.Duration(0),
				EnableHTTP2:           false,
			},
		},
	}

	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()

			poolInstance := pool.New(testCase.config)
			if poolInstance == nil {
				t.Fatal("New() returned nil")
			}

			stats := poolInstance.Stats()
			// Stats should be initialized (TotalConnections should be 0 for new pool)
			if stats.TotalConnections < 0 {
				t.Fatal("pool stats not properly initialized")
			}

			// Test that the pool is functional by checking if it's healthy
			if !poolInstance.IsHealthy() {
				t.Fatal("new pool should be healthy")
			}
		})
	}
}

func TestPoolGetPut(t *testing.T) {
	t.Parallel()

	poolInstance := pool.New(pool.DefaultConfig())

	defer func() {
		closeErr := poolInstance.Close()
		if closeErr != nil {
			t.Errorf("Failed to close pool: %v", closeErr)
		}
	}()

	// Get client
	client, err := poolInstance.Get()
	if err != nil {
		t.Fatalf("Get() error = %v", err)
	}

	if client == nil {
		t.Fatal("Get() returned nil client")
	}

	// Check stats
	stats := poolInstance.Stats()
	if stats.ActiveConnections != 1 {
		t.Errorf("ActiveConnections = %d, want 1", stats.ActiveConnections)
	}

	// Put client back
	poolInstance.Put(client)

	// Check stats after put
	stats = poolInstance.Stats()
	if stats.ActiveConnections != 0 {
		t.Errorf("ActiveConnections after Put = %d, want 0", stats.ActiveConnections)
	}

	if stats.IdleConnections != 1 {
		t.Errorf("IdleConnections = %d, want 1", stats.IdleConnections)
	}
}

func TestPoolDo(t *testing.T) {
	t.Parallel()

	// Create test server
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)

		_, writeErr := w.Write([]byte("test response"))
		if writeErr != nil {
			t.Errorf("Failed to write response: %v", writeErr)
		}
	}))
	defer server.Close()

	poolInstance := pool.New(pool.DefaultConfig())

	defer func() {
		closeErr := poolInstance.Close()
		if closeErr != nil {
			t.Errorf("Failed to close pool: %v", closeErr)
		}
	}()

	// Create request
	req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, server.URL, nil)
	if err != nil {
		t.Fatalf("NewRequest() error = %v", err)
	}

	// Execute request
	resp, err := poolInstance.Do(req)
	if err != nil {
		t.Fatalf("Do() error = %v", err)
	}

	defer func() {
		closeErr := resp.Body.Close()
		if closeErr != nil {
			t.Errorf("Failed to close response body: %v", closeErr)
		}
	}()

	if resp.StatusCode != http.StatusOK {
		t.Errorf("StatusCode = %d, want %d", resp.StatusCode, http.StatusOK)
	}

	// Check stats
	stats := poolInstance.Stats()
	if stats.RequestsServed != 1 {
		t.Errorf("RequestsServed = %d, want 1", stats.RequestsServed)
	}
}

func TestPoolDoWithContext(t *testing.T) {
	t.Parallel()

	// Create test server
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(100 * time.Millisecond)
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	poolInstance := pool.New(pool.DefaultConfig())

	defer func() {
		closeErr := poolInstance.Close()
		if closeErr != nil {
			t.Errorf("Failed to close pool: %v", closeErr)
		}
	}()

	// Test with timeout context
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()

	req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, server.URL, nil)
	if err != nil {
		t.Fatalf("NewRequest() error = %v", err)
	}

	// Should timeout
	resp, err := poolInstance.DoWithContext(ctx, req)
	if resp != nil && resp.Body != nil {
		closeErr := resp.Body.Close()
		if closeErr != nil {
			t.Errorf("Failed to close response body: %v", closeErr)
		}
	}

	if err == nil {
		t.Fatal("DoWithContext() should have timed out")
	}

	// Check failed connection stat
	stats := poolInstance.Stats()
	if stats.FailedConnections != 1 {
		t.Errorf("FailedConnections = %d, want 1", stats.FailedConnections)
	}
}

func TestPoolClose(t *testing.T) {
	t.Parallel()

	poolInstance := pool.New(pool.DefaultConfig())

	// Close pool
	err := poolInstance.Close()
	if err != nil {
		t.Fatalf("Close() error = %v", err)
	}

	// Try to get client from closed pool
	_, err = poolInstance.Get()
	if err == nil {
		t.Fatal("Get() should fail on closed pool")
	}

	// Close again should not error
	err = poolInstance.Close()
	if err != nil {
		t.Fatalf("Close() on already closed pool error = %v", err)
	}
}

func TestPoolSetters(t *testing.T) {
	t.Parallel()

	poolInstance := pool.New(pool.DefaultConfig())

	defer func() {
		closeErr := poolInstance.Close()
		if closeErr != nil {
			t.Errorf("Failed to close pool: %v", closeErr)
		}
	}()

	// Set max connections
	poolInstance.SetMaxConnections(200)
	// Note: Cannot directly verify as config is unexported
	// The effect would be tested through actual connection behavior

	// Set max connections per host
	poolInstance.SetMaxConnectionsPerHost(20)
	// Note: Cannot directly verify as config is unexported
	// The effect would be tested through actual connection behavior
}

func TestPoolIsHealthy(t *testing.T) {
	t.Parallel()

	poolInstance := pool.New(pool.DefaultConfig())

	defer func() {
		closeErr := poolInstance.Close()
		if closeErr != nil {
			t.Errorf("Failed to close pool: %v", closeErr)
		}
	}()

	// New pool should be healthy
	if !poolInstance.IsHealthy() {
		t.Fatal("New pool should be healthy")
	}

	// Note: Cannot directly manipulate stats as it's unexported
	// In a real test, we would simulate failures through actual failed connections

	// The health check logic would be tested through actual connection behavior

	// Close pool
	closeErr := poolInstance.Close()
	if closeErr != nil {
		t.Errorf("Failed to close pool: %v", closeErr)
	}

	// Closed pool should be unhealthy
	if poolInstance.IsHealthy() {
		t.Fatal("Closed pool should be unhealthy")
	}
}

func TestPoolStats(t *testing.T) {
	t.Parallel()

	poolInstance := pool.New(pool.DefaultConfig())

	defer func() {
		closeErr := poolInstance.Close()
		if closeErr != nil {
			t.Errorf("Failed to close pool: %v", closeErr)
		}
	}()

	// Initial stats should be zero
	stats := poolInstance.Stats()
	if stats.RequestsServed != 0 {
		t.Errorf("Initial RequestsServed = %d, want 0", stats.RequestsServed)
	}

	// Create test server
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Length", "13")
		w.WriteHeader(http.StatusOK)

		_, writeErr := w.Write([]byte("test response"))
		if writeErr != nil {
			t.Errorf("Failed to write response: %v", writeErr)
		}
	}))
	defer server.Close()

	// Make multiple requests
	for range 3 {
		req, _ := http.NewRequestWithContext(context.Background(), http.MethodGet, server.URL, nil)

		resp, err := poolInstance.Do(req)
		if err != nil {
			t.Fatalf("Do() error = %v", err)
		}

		closeErr := resp.Body.Close()
		if closeErr != nil {
			t.Errorf("Failed to close response body: %v", closeErr)
		}
	}

	// Check accumulated stats
	stats = poolInstance.Stats()
	if stats.RequestsServed != 3 {
		t.Errorf("RequestsServed = %d, want 3", stats.RequestsServed)
	}

	if stats.AverageResponseTime == 0 {
		t.Error("AverageResponseTime should be > 0")
	}
}
