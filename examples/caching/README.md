# Request Caching Example

This example demonstrates the request caching feature that improves performance by caching GET request responses.

## Overview

Request caching reduces API load and improves performance by storing responses from idempotent GET requests. The cache uses an LRU (Least Recently Used) eviction policy with TTL (Time-To-Live) expiration.

## Features

- **Automatic Caching**: GET requests are automatically cached when enabled
- **LRU Eviction**: Least recently used entries are evicted when cache is full
- **TTL Expiration**: Entries expire after configured time period
- **Pattern Invalidation**: Invalidate cache entries by URL pattern (wildcards supported)
- **Thread-Safe**: Concurrent access is safe across goroutines
- **Statistics**: Real-time metrics for monitoring cache effectiveness

## Usage

### Basic Cache Configuration

```go
import pve "github.com/fivetwenty-io/proxmox-apiclient-go/v3/pkg/client"

cacheConfig := pve.CacheConfig{
    Enabled:         true,
    MaxSize:         50 * 1024 * 1024,  // 50 MB
    DefaultTTL:      5 * time.Minute,   // Cache for 5 minutes
    CleanupInterval: 1 * time.Minute,   // Cleanup every minute
}

client, _ := pve.NewClient(pve.Options{
    Host:        "pve.example.com",
    Username:    "root@pam",
    Password:    "your-password",
    CacheConfig: &cacheConfig,
})
```

### Making Cached Requests

```go
// First request - cache miss (hits API)
start := time.Now()
version, _ := client.Get("/version", nil)
firstDuration := time.Since(start)

// Second request - cache hit (served from cache)
start = time.Now()
version, _ = client.Get("/version", nil)
secondDuration := time.Since(start)

// Cache typically provides 10-100x speedup
```

### Cache Statistics

```go
stats := client.CacheStats()
fmt.Printf("Hits:      %d\n", stats.Hits)
fmt.Printf("Misses:    %d\n", stats.Misses)
fmt.Printf("Hit Rate:  %.2f%%\n", stats.HitRate * 100)
fmt.Printf("Size:      %d bytes\n", stats.Size)
fmt.Printf("Entries:   %d\n", stats.Entries)
```

### Cache Invalidation

```go
// Invalidate specific pattern
removed := client.InvalidateCache("/nodes/*")
fmt.Printf("Removed %d entries\n", removed)

// Clear entire cache
client.ClearCache()
```

## Configuration Options

| Option | Type | Default | Description |
|--------|------|---------|-------------|
| Enabled | bool | false | Enable/disable caching (opt-in) |
| MaxSize | int64 | 100 MB | Maximum cache size in bytes |
| DefaultTTL | time.Duration | 5 minutes | Default entry lifetime |
| CleanupInterval | time.Duration | 1 minute | How often to remove expired entries |

## Cache Behavior

### What Gets Cached

- ✅ **GET requests** - Idempotent read operations
- ✅ **2xx responses** - Successful responses only
- ❌ **POST/PUT/DELETE** - Write operations never cached
- ❌ **Error responses** - Failed requests not cached

### When Entries Are Removed

1. **TTL Expiration**: Entry exceeds configured TTL
2. **LRU Eviction**: Cache exceeds MaxSize, oldest entries removed
3. **Pattern Invalidation**: Manual invalidation with `InvalidateCache()`
4. **Clear All**: Manual clear with `ClearCache()`
5. **Background Cleanup**: Expired entries removed periodically

## Performance Guidelines

### Recommended Settings by Use Case

**Dashboard/Monitoring Applications**:
```go
CacheConfig{
    Enabled:         true,
    MaxSize:         50 * 1024 * 1024,   // 50 MB
    DefaultTTL:      30 * time.Second,   // 30 seconds
    CleanupInterval: 10 * time.Second,
}
```

**Batch Processing**:
```go
CacheConfig{
    Enabled:         true,
    MaxSize:         200 * 1024 * 1024,  // 200 MB
    DefaultTTL:      10 * time.Minute,   // 10 minutes
    CleanupInterval: 2 * time.Minute,
}
```

**Memory-Constrained Environments**:
```go
CacheConfig{
    Enabled:         true,
    MaxSize:         10 * 1024 * 1024,   // 10 MB
    DefaultTTL:      2 * time.Minute,
    CleanupInterval: 30 * time.Second,
}
```

**Development/Testing**:
```go
CacheConfig{
    Enabled:         true,
    MaxSize:         5 * 1024 * 1024,    // 5 MB
    DefaultTTL:      10 * time.Second,   // Very short TTL
    CleanupInterval: 5 * time.Second,
}
```

## Pattern Matching

Cache invalidation supports wildcard patterns:

```go
// Wildcard at end matches all with prefix
client.InvalidateCache("/nodes/*")        // Matches /nodes/pve1, /nodes/pve2/status, etc.
client.InvalidateCache("/storage/local/*") // Matches all under /storage/local/

// Exact match (no wildcard)
client.InvalidateCache("/version")         // Matches only /version
```

## Best Practices

### When to Enable Caching

✅ **Good Candidates**:
- Read-heavy workloads (monitoring, dashboards)
- Frequently accessed static data (version, cluster info)
- Repeated identical requests (batch processing)
- High-latency network environments

❌ **Avoid Caching**:
- Write-heavy workloads
- Real-time critical applications requiring fresh data
- Very memory-constrained environments (< 10 MB available)

### Cache Invalidation Strategy

**Reactive Invalidation**:
```go
// Invalidate after writes
_, _ = client.Post("/nodes/pve1/qemu/100/start", nil)
client.InvalidateCache("/nodes/pve1/qemu/100/*")
```

**Time-Based**:
```go
// Use short TTL for dynamic data
config.DefaultTTL = 30 * time.Second
```

**Pattern-Based**:
```go
// Invalidate related resources
client.Post("/storage/local/content", params)
client.InvalidateCache("/storage/local/*")
```

### Monitoring Cache Effectiveness

```go
// Periodically check cache performance
ticker := time.NewTicker(1 * time.Minute)
go func() {
    for range ticker.C {
        stats := client.CacheStats()
        if stats != nil && stats.HitRate < 0.5 {
            log.Printf("Warning: Low cache hit rate: %.2f%%", stats.HitRate*100)
        }
    }
}()
```

## Running the Example

```bash
# Basic run
go run main.go

# With race detector (verify thread-safety)
go run -race main.go

# Build and run
go build -o caching-example
./caching-example
```

## Integration with Other Features

### With Auto-Login

```go
client, _ := pve.NewClient(pve.Options{
    Host:        "pve.example.com",
    Username:    "root@pam",
    Password:    "your-password",
    AutoLogin:   true,           // Auto-login on first request
    CacheConfig: &cacheConfig,   // Caching enabled
})

// First request triggers login + caches response
// Subsequent requests served from cache (no authentication overhead)
```

### With Context Detection

```go
client, _ := pve.NewClient(pve.Options{
    Host:           "localhost",
    Username:       "root@pam",
    Password:       "secret",
    AutoDetectMode: true,         // Detect execution context
    CacheConfig:    &cacheConfig, // Caching works in both local and remote modes
})
```

## Troubleshooting

### Cache Not Working

**Check 1**: Verify caching is enabled
```go
if config.Enabled {
    log.Println("Caching enabled")
}
```

**Check 2**: Verify GET requests
```go
// Only GET requests are cached
resp, _ := client.Get("/version", nil)  // Cached ✓
resp, _ := client.Post("/version", nil) // Not cached ✗
```

**Check 3**: Check statistics
```go
stats := client.CacheStats()
if stats == nil {
    log.Println("Caching not configured")
}
```

### Low Hit Rate

**Symptom**: `HitRate < 0.5` (less than 50%)

**Possible Causes**:
1. TTL too short (data expires before reuse)
2. Unique requests (no repeated patterns)
3. Cache too small (frequent evictions)
4. Excessive invalidation

**Solutions**:
```go
// Increase TTL
config.DefaultTTL = 10 * time.Minute

// Increase cache size
config.MaxSize = 100 * 1024 * 1024

// Review invalidation patterns
```

### High Memory Usage

**Symptom**: Cache using more memory than expected

**Solutions**:
```go
// Reduce max size
config.MaxSize = 20 * 1024 * 1024

// Reduce TTL (faster expiration)
config.DefaultTTL = 2 * time.Minute

// Increase cleanup frequency
config.CleanupInterval = 30 * time.Second
```

## See Also

- [Auto-Login Example](../auto-login/) - Automatic authentication
- [Context Detection Example](../context-detection/) - Execution context awareness
- [Main Documentation](../../README.md) - Full client documentation
- [Cache Package Docs](../../pkg/cache/) - Cache implementation details
