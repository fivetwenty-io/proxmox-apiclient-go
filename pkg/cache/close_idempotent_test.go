package cache_test

import (
	"testing"
	"time"

	"github.com/fivetwenty-io/proxmox-apiclient-go/v3/pkg/cache"
)

// TestCovCloseIdempotentDisabled verifies that calling Close() twice on a
// disabled cache does not panic (no cleanup goroutine running).
func TestCovCloseIdempotentDisabled(t *testing.T) {
	t.Parallel()

	cfg := cache.DefaultConfig()
	cfg.Enabled = false

	c := cache.NewCache(cfg)

	c.Close()
	c.Close() // must not panic
}

// TestCovCloseIdempotentEnabled verifies that calling Close() twice on an
// enabled cache (with a running cleanup goroutine) does not panic. The
// sync.Once guard inside Cache.Close must protect the channel close.
func TestCovCloseIdempotentEnabled(t *testing.T) {
	t.Parallel()

	cfg := cache.DefaultConfig()
	cfg.Enabled = true
	cfg.CleanupInterval = 10 * time.Second // long interval — goroutine stays alive

	c := cache.NewCache(cfg)

	c.Close()
	c.Close() // must not panic
}
