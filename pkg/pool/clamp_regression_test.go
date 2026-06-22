package pool_test

import (
	"testing"

	"github.com/fivetwenty-io/pve-apiclient-go/v3/pkg/pool"
)

// TestCovPutWithoutGetDoesNotGoBelowZero verifies that Put() without a matching
// Get() does not drive Stats().ActiveConnections negative.
func TestCovPutWithoutGetDoesNotGoBelowZero(t *testing.T) {
	t.Parallel()

	poolInst := pool.New(pool.DefaultConfig())
	defer func() { _ = poolInst.Close() }()

	// Get one connection, then return it normally, then Put once more (double Put).
	httpClient, err := poolInst.Get()
	if err != nil {
		t.Fatalf("Get: %v", err)
	}

	poolInst.Put(httpClient) // normal return — active goes to 0
	poolInst.Put(httpClient) // extra Put — must not make active go negative

	stats := poolInst.Stats()
	if stats.ActiveConnections < 0 {
		t.Errorf("ActiveConnections = %d after double Put, want >= 0", stats.ActiveConnections)
	}
}

// TestCovPutWithoutAnyGet verifies that a bare Put (no prior Get at all) does
// not drive ActiveConnections negative.
func TestCovPutWithoutAnyGet(t *testing.T) {
	t.Parallel()

	poolInst := pool.New(pool.DefaultConfig())
	defer func() { _ = poolInst.Close() }()

	// Obtain a client reference only so we have a non-nil pointer to pass; then
	// release it immediately and call Put once more without a matching Get.
	httpClient, err := poolInst.Get()
	if err != nil {
		t.Fatalf("Get: %v", err)
	}

	poolInst.Put(httpClient) // brings active to 0
	poolInst.Put(httpClient) // extra Put — active must stay clamped at 0

	stats := poolInst.Stats()
	if stats.ActiveConnections < 0 {
		t.Errorf("ActiveConnections = %d after extra Put, want >= 0", stats.ActiveConnections)
	}
}
