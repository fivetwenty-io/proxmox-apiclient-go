package cache_test

import (
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/fivetwenty-io/proxmox-apiclient-go/v3/pkg/cache"
)

func TestCache_BasicGetSet(t *testing.T) {
	t.Parallel()

	config := cache.DefaultConfig()
	config.Enabled = true

	testCache := cache.NewCache(config)
	defer testCache.Close()

	// Test Set and Get
	testCache.Set("key1", "value1", 1*time.Minute)

	val, found := testCache.Get("key1")
	if !found {
		t.Fatal("Expected to find key1")
	}

	if valStr, ok := val.(string); !ok || valStr != "value1" {
		t.Errorf("Expected 'value1', got %v", val)
	}

	// Test Get non-existent key
	_, found = testCache.Get("nonexistent")
	if found {
		t.Error("Expected not to find nonexistent key")
	}
}

func TestCache_TTLExpiration(t *testing.T) {
	t.Parallel()

	config := cache.DefaultConfig()
	config.Enabled = true
	config.DefaultTTL = 100 * time.Millisecond

	testCache := cache.NewCache(config)
	defer testCache.Close()

	// Set with short TTL
	testCache.Set("expire-key", "expire-value", 50*time.Millisecond)

	// Should be available immediately
	val, found := testCache.Get("expire-key")
	if !found {
		t.Fatal("Expected to find expire-key immediately")
	}

	if valStr, ok := val.(string); !ok || valStr != "expire-value" {
		t.Errorf("Expected 'expire-value', got %v", val)
	}

	// Wait for expiration
	time.Sleep(100 * time.Millisecond)

	// Should be expired
	_, found = testCache.Get("expire-key")
	if found {
		t.Error("Expected expire-key to be expired")
	}

	// Check that miss counter increased
	stats := testCache.Stats()
	if stats.Misses != 1 {
		t.Errorf("Expected 1 miss, got %d", stats.Misses)
	}
}

func TestCache_LRUEviction(t *testing.T) {
	t.Parallel()

	// Values are sized so that estimateSize (len(string)) makes each entry
	// ~1KB; 3 entries exactly fill MaxSize, so a 4th forces an eviction.
	value1 := strings.Repeat("a", 1024)
	value2 := strings.Repeat("b", 1024)
	value3 := strings.Repeat("c", 1024)
	value4 := strings.Repeat("d", 1024)

	config := cache.DefaultConfig()
	config.Enabled = true
	config.MaxSize = 3 * 1024 // 3KB total (3 entries with ~1KB each)

	testCache := cache.NewCache(config)
	defer testCache.Close()

	// Add 3 entries (fills cache)
	testCache.Set("key1", value1, 1*time.Minute)
	testCache.Set("key2", value2, 1*time.Minute)
	testCache.Set("key3", value3, 1*time.Minute)

	// Access in order: key1, key2, key3 (key1 becomes LRU)
	if _, found := testCache.Get("key1"); !found {
		t.Error("Expected key1 to be present")
	}

	if _, found := testCache.Get("key2"); !found {
		t.Error("Expected key2 to be present")
	}

	if _, found := testCache.Get("key3"); !found {
		t.Error("Expected key3 to be present")
	}

	// Add 4th entry (should evict oldest which is key1 since it was accessed first)
	testCache.Set("key4", value4, 1*time.Minute)

	// key1 should be evicted (it's the LRU)
	if _, found := testCache.Get("key1"); found {
		t.Error("Expected key1 to be evicted")
	}

	// Others should still be present
	if _, found := testCache.Get("key2"); !found {
		t.Error("Expected key2 to be present after eviction")
	}

	if _, found := testCache.Get("key3"); !found {
		t.Error("Expected key3 to be present after eviction")
	}

	if _, found := testCache.Get("key4"); !found {
		t.Error("Expected key4 to be present after eviction")
	}

	stats := testCache.Stats()
	if stats.Evictions < 1 {
		t.Errorf("Expected at least 1 eviction, got %d", stats.Evictions)
	}
}

// TestCache_SizeBasedEviction_RealisticValues verifies that estimateSize is
// type-aware: a cache sized to hold one large value plus a couple of small
// values should not evict on Set until the large value alone would exceed
// MaxSize, and adding the large value should evict enough small values to fit.
func TestCache_SizeBasedEviction_RealisticValues(t *testing.T) {
	t.Parallel()

	smallValue := "x" // 1 byte
	largeValue := strings.Repeat("y", 4096)

	config := cache.DefaultConfig()
	config.Enabled = true
	config.MaxSize = 4096 + 4 // room for the large value plus a few small ones, not all five

	testCache := cache.NewCache(config)
	defer testCache.Close()

	// Several tiny values fit comfortably alongside each other under a byte-
	// accurate size model; a flat-1KB-per-entry model would have evicted
	// after 4 entries even though the actual bytes stored are tiny.
	testCache.Set("small1", smallValue, time.Minute)
	testCache.Set("small2", smallValue, time.Minute)
	testCache.Set("small3", smallValue, time.Minute)
	testCache.Set("small4", smallValue, time.Minute)
	testCache.Set("small5", smallValue, time.Minute)

	stats := testCache.Stats()
	if stats.Evictions != 0 {
		t.Errorf("small values: want 0 evictions, got %d (stats.Size=%d)", stats.Evictions, stats.Size)
	}

	if stats.Entries != 5 {
		t.Errorf("small values: want 5 entries, got %d", stats.Entries)
	}

	// Adding the large value must evict enough small entries to stay within
	// MaxSize.
	testCache.Set("large", largeValue, time.Minute)

	stats = testCache.Stats()
	if stats.Size > config.MaxSize {
		t.Errorf("size after large Set: want <= %d, got %d", config.MaxSize, stats.Size)
	}

	if stats.Evictions == 0 {
		t.Error("large value: want at least 1 eviction to make room")
	}

	if _, found := testCache.Get("large"); !found {
		t.Error("large value: want present after Set")
	}
}

// selfSizingValue reports a fixed footprint through the cache.Sizer interface.
type selfSizingValue struct {
	payload []byte
}

func (v *selfSizingValue) CacheSize() int64 {
	return int64(len(v.payload))
}

func TestCache_SizerValues_UseReportedSize(t *testing.T) {
	t.Parallel()

	config := cache.DefaultConfig()
	config.Enabled = true
	config.MaxSize = 100

	testCache := cache.NewCache(config)
	defer testCache.Close()

	// A Sizer-reported 90-byte value fits within the 100-byte budget; a
	// JSON-based estimate of the same struct would base64-inflate the payload
	// and overshoot it.
	testCache.Set("sized", &selfSizingValue{payload: make([]byte, 90)}, time.Minute)

	stats := testCache.Stats()
	if stats.Size != 90 {
		t.Errorf("Sizer value: want accounted size 90, got %d", stats.Size)
	}

	if stats.Evictions != 0 {
		t.Errorf("Sizer value: want 0 evictions, got %d", stats.Evictions)
	}
}

func TestCache_ConcurrentAccess(t *testing.T) {
	t.Parallel()

	config := cache.DefaultConfig()
	config.Enabled = true

	testCache := cache.NewCache(config)
	defer testCache.Close()

	const (
		goroutines = 100
		iterations = 100
	)

	var waitGroup sync.WaitGroup
	waitGroup.Add(goroutines)

	// Concurrent writes
	for goroutineID := range goroutines {
		go func(id int) {
			defer waitGroup.Done()

			for range iterations {
				key := "concurrent-" + string(rune(id))
				testCache.Set(key, id, 1*time.Minute)
			}
		}(goroutineID)
	}

	waitGroup.Wait()

	// Concurrent reads
	waitGroup.Add(goroutines)

	for goroutineID := range goroutines {
		go func(id int) {
			defer waitGroup.Done()

			for range iterations {
				key := "concurrent-" + string(rune(id))
				testCache.Get(key)
			}
		}(goroutineID)
	}

	waitGroup.Wait()

	// Should have entries
	stats := testCache.Stats()
	if stats.Entries == 0 {
		t.Error("Expected some entries after concurrent access")
	}
}

func TestCache_PatternInvalidation(t *testing.T) {
	t.Parallel()

	config := cache.DefaultConfig()
	config.Enabled = true

	testCache := cache.NewCache(config)
	defer testCache.Close()

	// Add entries with different path patterns
	testCache.Set("/nodes/node1/status", "data1", 1*time.Minute)
	testCache.Set("/nodes/node2/status", "data2", 1*time.Minute)
	testCache.Set("/storage/local/status", "data3", 1*time.Minute)
	testCache.Set("/version", "data4", 1*time.Minute)

	// Invalidate all /nodes/* entries
	removed := testCache.Invalidate("/nodes/*")
	if removed != 2 {
		t.Errorf("Expected to invalidate 2 entries, got %d", removed)
	}

	// /nodes entries should be gone
	if _, found := testCache.Get("/nodes/node1/status"); found {
		t.Error("Expected /nodes/node1/status to be invalidated")
	}

	if _, found := testCache.Get("/nodes/node2/status"); found {
		t.Error("Expected /nodes/node2/status to be invalidated")
	}

	// Others should remain
	if _, found := testCache.Get("/storage/local/status"); !found {
		t.Error("Expected /storage/local/status to remain")
	}

	if _, found := testCache.Get("/version"); !found {
		t.Error("Expected /version to remain")
	}
}

func TestCache_ExactInvalidation(t *testing.T) {
	t.Parallel()

	config := cache.DefaultConfig()
	config.Enabled = true

	testCache := cache.NewCache(config)
	defer testCache.Close()

	testCache.Set("exact-key", "value", 1*time.Minute)
	testCache.Set("exact-key-other", "value2", 1*time.Minute)

	// Exact match invalidation (no wildcard)
	removed := testCache.Invalidate("exact-key")
	if removed != 1 {
		t.Errorf("Expected to invalidate 1 entry, got %d", removed)
	}

	if _, found := testCache.Get("exact-key"); found {
		t.Error("Expected exact-key to be invalidated")
	}

	if _, found := testCache.Get("exact-key-other"); !found {
		t.Error("Expected exact-key-other to remain")
	}
}

func TestCache_Clear(t *testing.T) {
	t.Parallel()

	config := cache.DefaultConfig()
	config.Enabled = true

	testCache := cache.NewCache(config)
	defer testCache.Close()

	// Add multiple entries
	testCache.Set("key1", "value1", 1*time.Minute)
	testCache.Set("key2", "value2", 1*time.Minute)
	testCache.Set("key3", "value3", 1*time.Minute)

	stats := testCache.Stats()
	if stats.Entries != 3 {
		t.Errorf("Expected 3 entries, got %d", stats.Entries)
	}

	// Clear all
	testCache.Clear()

	stats = testCache.Stats()
	if stats.Entries != 0 {
		t.Errorf("Expected 0 entries after Clear, got %d", stats.Entries)
	}

	if stats.Size != 0 {
		t.Errorf("Expected 0 size after Clear, got %d", stats.Size)
	}
}

func TestCache_Stats(t *testing.T) {
	t.Parallel()

	config := cache.DefaultConfig()
	config.Enabled = true

	testCache := cache.NewCache(config)
	defer testCache.Close()

	// Initial stats
	stats := testCache.Stats()
	if stats.Hits != 0 || stats.Misses != 0 || stats.Evictions != 0 {
		t.Error("Expected all stats to be 0 initially")
	}

	// Add entry and hit it
	testCache.Set("key1", "value1", 1*time.Minute)
	testCache.Get("key1") // hit
	testCache.Get("key1") // hit

	stats = testCache.Stats()
	if stats.Hits != 2 {
		t.Errorf("Expected 2 hits, got %d", stats.Hits)
	}

	if stats.HitRate != 1.0 {
		t.Errorf("Expected hit rate 1.0, got %f", stats.HitRate)
	}

	// Miss
	testCache.Get("nonexistent")

	stats = testCache.Stats()
	if stats.Misses != 1 {
		t.Errorf("Expected 1 miss, got %d", stats.Misses)
	}

	expectedHitRate := 2.0 / 3.0
	if stats.HitRate < expectedHitRate-0.01 || stats.HitRate > expectedHitRate+0.01 {
		t.Errorf("Expected hit rate ~0.67, got %f", stats.HitRate)
	}
}

func TestCache_Disabled(t *testing.T) {
	t.Parallel()

	config := cache.DefaultConfig()
	config.Enabled = false // Disabled

	testCache := cache.NewCache(config)
	defer testCache.Close()

	// Operations should be no-ops
	testCache.Set("key1", "value1", 1*time.Minute)

	_, found := testCache.Get("key1")
	if found {
		t.Error("Expected cache to be disabled, but Get returned found=true")
	}

	stats := testCache.Stats()
	if stats.Entries != 0 {
		t.Error("Expected 0 entries when cache is disabled")
	}
}

func TestCache_UpdateExisting(t *testing.T) {
	t.Parallel()

	config := cache.DefaultConfig()
	config.Enabled = true

	testCache := cache.NewCache(config)
	defer testCache.Close()

	// Set initial value
	testCache.Set("key1", "value1", 1*time.Minute)

	val, found := testCache.Get("key1")

	valStr, ok := val.(string)
	if !found || !ok || valStr != "value1" {
		t.Fatal("Expected to find initial value")
	}

	// Update with new value
	testCache.Set("key1", "value2", 1*time.Minute)

	val, found = testCache.Get("key1")
	if !found {
		t.Fatal("Expected to find updated value")
	}

	if valStr, ok := val.(string); !ok || valStr != "value2" {
		t.Errorf("Expected 'value2', got %v", val)
	}

	// Should still have only 1 entry
	stats := testCache.Stats()
	if stats.Entries != 1 {
		t.Errorf("Expected 1 entry after update, got %d", stats.Entries)
	}
}

func TestCache_CleanupLoop(t *testing.T) {
	t.Parallel()

	config := cache.DefaultConfig()
	config.Enabled = true
	config.CleanupInterval = 50 * time.Millisecond

	testCache := cache.NewCache(config)
	defer testCache.Close()

	// Add entries with short TTL
	testCache.Set("cleanup1", "value1", 30*time.Millisecond)
	testCache.Set("cleanup2", "value2", 30*time.Millisecond)
	testCache.Set("cleanup3", "value3", 5*time.Minute) // This one won't expire

	// Wait for cleanup to run
	time.Sleep(150 * time.Millisecond)

	// Expired entries should be cleaned up
	if _, found := testCache.Get("cleanup1"); found {
		t.Error("Expected cleanup1 to be cleaned up")
	}

	if _, found := testCache.Get("cleanup2"); found {
		t.Error("Expected cleanup2 to be cleaned up")
	}

	// Non-expired should remain
	if _, found := testCache.Get("cleanup3"); !found {
		t.Error("Expected cleanup3 to remain")
	}
}

func TestGenerateKey(t *testing.T) {
	t.Parallel()

	const (
		paramNode  = "node"
		nodeNameV1 = "pve1"
	)

	// Test consistent key generation
	key1 := cache.GenerateKey("GET", "/nodes", map[string]interface{}{paramNode: nodeNameV1})
	key2 := cache.GenerateKey("GET", "/nodes", map[string]interface{}{paramNode: nodeNameV1})

	if key1 != key2 {
		t.Error("Expected identical keys for identical inputs")
	}

	// Different method should produce different key
	key3 := cache.GenerateKey("POST", "/nodes", map[string]interface{}{paramNode: nodeNameV1})
	if key1 == key3 {
		t.Error("Expected different keys for different methods")
	}

	// Different path should produce different key
	key4 := cache.GenerateKey("GET", "/storage", map[string]interface{}{paramNode: nodeNameV1})
	if key1 == key4 {
		t.Error("Expected different keys for different paths")
	}

	// Different params should produce different key
	key5 := cache.GenerateKey("GET", "/nodes", map[string]interface{}{paramNode: "pve2"})
	if key1 == key5 {
		t.Error("Expected different keys for different params")
	}

	// Param order shouldn't matter (sorted internally)
	key6 := cache.GenerateKey("GET", "/nodes", map[string]interface{}{
		paramNode: nodeNameV1,
		"type":    "qemu",
	})

	key7 := cache.GenerateKey("GET", "/nodes", map[string]interface{}{
		"type":    "qemu",
		paramNode: nodeNameV1,
	})
	if key6 != key7 {
		t.Error("Expected identical keys regardless of param order")
	}
}

func TestGenerateKeyFromURL(t *testing.T) {
	t.Parallel()

	key1 := cache.GenerateKeyFromURL("GET", "https://pve.example.com/api2/json/nodes/pve1")
	key2 := cache.GenerateKeyFromURL("GET", "https://pve.example.com/api2/json/nodes/pve1")

	if key1 != key2 {
		t.Error("Expected identical keys for identical URLs")
	}

	key3 := cache.GenerateKeyFromURL("GET", "https://pve.example.com/api2/json/nodes/pve2")
	if key1 == key3 {
		t.Error("Expected different keys for different URLs")
	}
}

func TestShouldCache(t *testing.T) {
	t.Parallel()

	tests := []struct {
		method string
		want   bool
	}{
		{"GET", true},
		{"get", true},
		{"Get", true},
		{"POST", false},
		{"PUT", false},
		{"DELETE", false},
		{"PATCH", false},
	}

	for _, tt := range tests {
		t.Run(tt.method, func(t *testing.T) {
			t.Parallel()

			if got := cache.ShouldCache(tt.method); got != tt.want {
				t.Errorf("ShouldCache(%q) = %v, want %v", tt.method, got, tt.want)
			}
		})
	}
}
