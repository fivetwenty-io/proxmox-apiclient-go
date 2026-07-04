package cache

import (
	"testing"
	"time"
)

const corruptTestKey = "corrupt-key"

// newTestCache builds an enabled Cache with a long cleanup interval so the
// background goroutine does not race with the corrupt-element manipulation
// these tests perform directly against unexported fields.
func newTestCache(t *testing.T) *Cache {
	t.Helper()

	cfg := Config{
		MaxSize:         DefaultMaxCacheSizeMB,
		DefaultTTL:      time.Minute,
		CleanupInterval: time.Hour,
		Enabled:         true,
	}

	testCache := NewCache(cfg)
	t.Cleanup(testCache.Close)

	return testCache
}

// corruptEntry installs an LRU element under corruptTestKey whose Value is
// not a *cacheItem, simulating the internal invariant violation that Get,
// Set, removeElement, and cleanupExpired must degrade from instead of
// panicking on.
func corruptEntry(testCache *Cache) {
	testCache.mu.Lock()
	defer testCache.mu.Unlock()

	elem := testCache.lru.PushFront("not-a-cacheItem")
	testCache.entries[corruptTestKey] = elem
}

func TestGet_CorruptElement_DegradesGracefully(t *testing.T) {
	t.Parallel()

	testCache := newTestCache(t)

	corruptEntry(testCache)

	val, found := testCache.Get(corruptTestKey)
	if found {
		t.Errorf("Get on corrupt element: want not found, got value %v", val)
	}

	stats := testCache.Stats()
	if stats.Corrupted != 1 {
		t.Errorf("Corrupted: want 1, got %d", stats.Corrupted)
	}

	if stats.Misses != 1 {
		t.Errorf("Misses: want 1, got %d", stats.Misses)
	}

	testCache.mu.RLock()
	_, exists := testCache.entries[corruptTestKey]
	listLen := testCache.lru.Len()
	testCache.mu.RUnlock()

	if exists {
		t.Error("corrupt-key: want removed from entries index after Get")
	}

	if listLen != 0 {
		t.Errorf("lru length: want 0 after Get removes corrupt element, got %d", listLen)
	}
}

func TestSet_CorruptExistingElement_ReplacesGracefully(t *testing.T) {
	t.Parallel()

	testCache := newTestCache(t)

	corruptEntry(testCache)

	testCache.Set(corruptTestKey, "fresh-value", time.Minute)

	val, found := testCache.Get(corruptTestKey)
	if !found {
		t.Fatal("Set over corrupt element: want found after Set")
	}

	if val != "fresh-value" {
		t.Errorf("Set over corrupt element: want %q, got %v", "fresh-value", val)
	}

	stats := testCache.Stats()
	if stats.Corrupted != 1 {
		t.Errorf("Corrupted: want 1, got %d", stats.Corrupted)
	}

	if stats.Entries != 1 {
		t.Errorf("Entries: want 1, got %d", stats.Entries)
	}
}

func TestCleanupExpired_CorruptElement_DegradesGracefully(t *testing.T) {
	t.Parallel()

	testCache := newTestCache(t)

	corruptEntry(testCache)

	// Must not panic.
	testCache.cleanupExpired()

	stats := testCache.Stats()
	if stats.Corrupted != 1 {
		t.Errorf("Corrupted: want 1, got %d", stats.Corrupted)
	}

	testCache.mu.RLock()
	_, exists := testCache.entries[corruptTestKey]
	listLen := testCache.lru.Len()
	testCache.mu.RUnlock()

	if exists {
		t.Error("corrupt-key: want removed from entries index after cleanupExpired")
	}

	if listLen != 0 {
		t.Errorf("lru length: want 0 after cleanupExpired removes corrupt element, got %d", listLen)
	}
}

func TestEvictOldest_CorruptElement_DegradesGracefully(t *testing.T) {
	t.Parallel()

	testCache := newTestCache(t)

	corruptEntry(testCache)

	// Must not panic.
	testCache.evictOldest()

	stats := testCache.Stats()
	if stats.Corrupted != 1 {
		t.Errorf("Corrupted: want 1, got %d", stats.Corrupted)
	}

	if stats.Evictions != 1 {
		t.Errorf("Evictions: want 1, got %d", stats.Evictions)
	}
}
