package main

import (
	"fmt"
	"log"
	"time"

	pve "github.com/fivetwenty-io/proxmox-apiclient-go/v3/pkg/client"
)

const (
	// Cache size constants in bytes.
	DefaultCacheSizeMB = 50 * 1024 * 1024 // 50 MB
	CustomCacheSizeMB  = 10 * 1024 * 1024 // 10 MB

	// Cache TTL durations.
	DefaultCacheTTL = 5 * time.Minute  // 5 minutes
	CustomCacheTTL  = 30 * time.Second // 30 seconds

	// Cleanup intervals.
	DefaultCleanupInterval = 1 * time.Minute  // 1 minute
	CustomCleanupInterval  = 10 * time.Second // 10 seconds

	// Percentage multiplier for display.
	PercentageMultiplier = 100
)

func main() {
	fmt.Println("=== Request Caching Example ===")
	fmt.Println()

	client := demonstrateCacheSetup()
	demonstrateCachedRequests(client)
	demonstrateCacheStats(client)
	demonstrateCacheInvalidation(client)
	demonstrateCacheClear(client)
	demonstrateCustomConfig()

	printCachingSummary()
}

// demonstrateCacheSetup creates a client with caching enabled and displays configuration.
//
//nolint:ireturn // Example code - returns interface for demonstration
func demonstrateCacheSetup() pve.Client {
	fmt.Println("Example 1: Enable Request Caching")

	cacheConfig := pve.CacheConfig{
		Enabled:         true,
		MaxSize:         DefaultCacheSizeMB,     // 50 MB
		DefaultTTL:      DefaultCacheTTL,        // Cache entries for 5 minutes
		CleanupInterval: DefaultCleanupInterval, // Cleanup expired entries every minute
	}

	client, err := pve.NewClient(pve.Options{
		Host:        "pve.example.com",
		Username:    "root@pam",
		Password:    "your-password",
		CacheConfig: &cacheConfig,
	})
	if err != nil {
		log.Fatalf("Failed to create client: %v", err)
	}

	fmt.Println("✓ Client created with caching enabled")
	fmt.Println("  Max size: 50 MB")
	fmt.Println("  Default TTL: 5 minutes")
	fmt.Println()

	return client
}

// demonstrateCachedRequests shows cache miss and cache hit performance comparison.
func demonstrateCachedRequests(client pve.Client) {
	fmt.Println("Example 2: Cached vs Uncached Requests")

	// First request - cache miss (will hit the API)
	start := time.Now()

	version1, err := client.Get("/version", nil)
	if err != nil {
		log.Printf("First request failed: %v", err)
	}

	firstDuration := time.Since(start)

	fmt.Printf("First request (cache miss): %v\n", firstDuration)
	fmt.Printf("Response: %v\n", version1)

	// Second request - cache hit (served from cache)
	start = time.Now()

	version2, err := client.Get("/version", nil)
	if err != nil {
		log.Printf("Second request failed: %v", err)
	}

	secondDuration := time.Since(start)

	fmt.Printf("Second request (cache hit): %v\n", secondDuration)
	fmt.Printf("Response: %v\n", version2)

	// Show performance improvement
	if secondDuration < firstDuration {
		improvement := float64(firstDuration-secondDuration) / float64(firstDuration) * PercentageMultiplier
		fmt.Printf("Cache improved response time by %.1f%%\n", improvement)
	}

	fmt.Println()
}

// demonstrateCacheStats displays current cache statistics.
func demonstrateCacheStats(client pve.Client) {
	fmt.Println("Example 3: Cache Statistics")

	stats := client.CacheStats()
	if stats != nil {
		fmt.Printf("Cache Hits:      %d\n", stats.Hits)
		fmt.Printf("Cache Misses:    %d\n", stats.Misses)
		fmt.Printf("Hit Rate:        %.2f%%\n", stats.HitRate*PercentageMultiplier)
		fmt.Printf("Evictions:       %d\n", stats.Evictions)
		fmt.Printf("Current Size:    %d bytes\n", stats.Size)
		fmt.Printf("Total Entries:   %d\n", stats.Entries)
	}

	fmt.Println()
}

// demonstrateCacheInvalidation shows pattern-based cache invalidation.
func demonstrateCacheInvalidation(client pve.Client) {
	fmt.Println("Example 4: Cache Invalidation")

	// Make some requests to populate cache
	_, _ = client.Get("/nodes/pve1/status", nil)
	_, _ = client.Get("/nodes/pve2/status", nil)
	_, _ = client.Get("/storage/local/status", nil)

	fmt.Println("✓ Made requests to /nodes/* and /storage/*")

	// Invalidate all /nodes/* entries
	removed := client.InvalidateCache("/nodes/*")
	fmt.Printf("✓ Invalidated %d entries matching /nodes/*\n", removed)

	// Check stats again
	stats := client.CacheStats()
	if stats != nil {
		fmt.Printf("  Remaining entries: %d\n", stats.Entries)
	}

	fmt.Println()
}

// demonstrateCacheClear shows clearing all cache entries.
func demonstrateCacheClear(client pve.Client) {
	fmt.Println("Example 5: Clear All Cache")

	client.ClearCache()
	fmt.Println("✓ Cleared entire cache")

	stats := client.CacheStats()
	if stats != nil {
		fmt.Printf("  Entries after clear: %d\n", stats.Entries)
		fmt.Printf("  Size after clear: %d bytes\n", stats.Size)
	}

	fmt.Println()
}

// demonstrateCustomConfig shows custom cache configuration for specific scenarios.
func demonstrateCustomConfig() {
	fmt.Println("Example 6: Custom Cache Configuration")

	customConfig := pve.CacheConfig{
		Enabled:         true,
		MaxSize:         CustomCacheSizeMB,     // 10 MB (smaller cache)
		DefaultTTL:      CustomCacheTTL,        // Short TTL (30 seconds)
		CleanupInterval: CustomCleanupInterval, // Frequent cleanup
	}

	clientCustom, err := pve.NewClient(pve.Options{
		Host:        "pve.example.com",
		Username:    "root@pam",
		Password:    "your-password",
		CacheConfig: &customConfig,
	})
	if err != nil {
		log.Fatalf("Failed to create custom client: %v", err)
	}

	fmt.Println("✓ Client created with custom cache settings")
	fmt.Println("  Max size: 10 MB (memory-constrained environments)")
	fmt.Println("  Default TTL: 30 seconds (rapidly changing data)")
	fmt.Println("  Cleanup: 10 seconds (aggressive cleanup)")

	// Prevent unused variable warnings
	_ = clientCustom

	fmt.Println()
}

// printCachingSummary displays key takeaways and use cases for caching.
func printCachingSummary() {
	fmt.Println("=== Examples Complete ===")
	fmt.Println()
	fmt.Println("Key Points:")
	fmt.Println("1. Caching is opt-in (CacheConfig must be provided)")
	fmt.Println("2. Only GET requests are cached (idempotent operations)")
	fmt.Println("3. Pattern-based invalidation with wildcards (e.g., /nodes/*)")
	fmt.Println("4. LRU eviction when cache exceeds MaxSize")
	fmt.Println("5. TTL expiration for time-sensitive data")
	fmt.Println("6. Real-time statistics for monitoring cache effectiveness")
	fmt.Println()
	fmt.Println("Use Cases:")
	fmt.Println("- Reduce API load for frequently accessed data (version, status)")
	fmt.Println("- Improve performance for read-heavy workloads")
	fmt.Println("- Lower latency for dashboard/monitoring applications")
	fmt.Println("- Batch processing with repeated API calls")
}
