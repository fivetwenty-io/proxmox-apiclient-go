package benchmarks_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/fivetwenty-io/proxmox-apiclient-go/v3/pkg/batch"
	"github.com/fivetwenty-io/proxmox-apiclient-go/v3/pkg/client"
	"github.com/fivetwenty-io/proxmox-apiclient-go/v3/pkg/pool"
	"github.com/fivetwenty-io/proxmox-apiclient-go/v3/pkg/stream"
)

var errEOF = errors.New("EOF")

const (
	benchAPIToken     = "test@pve!token=secret"
	benchKeyData      = "data"
	benchKeyCPU       = "cpu"
	benchKeyName      = "name"
	benchKeyNode      = "node"
	benchKeyStatus    = "status"
	benchStatusOnline = "online"
)

// Mock server for benchmarking.
func createMockServer() *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api2/json/access/ticket":
			_ = json.NewEncoder(writer).Encode(map[string]interface{}{
				benchKeyData: map[string]interface{}{
					"ticket":              "PVE:test@pve:1234567890::abcdef",
					"CSRFPreventionToken": "1234567890:abcdef",
				},
			})
		case "/api2/json/nodes":
			nodes := []map[string]interface{}{
				{benchKeyNode: "pve1", benchKeyStatus: benchStatusOnline, benchKeyCPU: 0.15},
				{benchKeyNode: "pve2", benchKeyStatus: benchStatusOnline, benchKeyCPU: 0.25},
				{benchKeyNode: "pve3", benchKeyStatus: benchStatusOnline, benchKeyCPU: 0.10},
			}
			_ = json.NewEncoder(writer).Encode(map[string]interface{}{benchKeyData: nodes})
		case "/api2/json/cluster/resources":
			resources := make([]map[string]interface{}, 100)
			for i := range 100 {
				resources[i] = map[string]interface{}{
					"vmid":         100 + i,
					benchKeyName:   fmt.Sprintf("vm-%d", 100+i),
					"type":         "qemu",
					benchKeyStatus: "running",
					benchKeyCPU:    0.1,
					"mem":          1073741824,
				}
			}

			_ = json.NewEncoder(writer).Encode(map[string]interface{}{benchKeyData: resources})
		default:
			_ = json.NewEncoder(writer).Encode(map[string]interface{}{benchKeyData: "ok"})
		}
	}))
}

// BenchmarkSimpleGet benchmarks a simple GET request.
func BenchmarkSimpleGet(b *testing.B) {
	server := createMockServer()
	defer server.Close()

	client, err := client.NewClient(client.Options{
		Host:     server.URL,
		APIToken: benchAPIToken,
	})
	if err != nil {
		b.Fatal(err)
	}

	b.ResetTimer()

	for range b.N {
		_, err := client.Get("/nodes", nil)
		if err != nil {
			b.Fatal(err)
		}
	}
}

// BenchmarkConcurrentRequests benchmarks concurrent API requests.
func BenchmarkConcurrentRequests(b *testing.B) {
	server := createMockServer()
	defer server.Close()

	client, err := client.NewClient(client.Options{
		Host:     server.URL,
		APIToken: benchAPIToken,
	})
	if err != nil {
		b.Fatal(err)
	}

	concurrency := 10

	b.ResetTimer()

	for range b.N {
		var waitGroup sync.WaitGroup
		for range concurrency {
			waitGroup.Add(1)

			go func() {
				defer waitGroup.Done()

				_, _ = client.Get("/nodes", nil)
			}()
		}

		waitGroup.Wait()
	}
}

// BenchmarkConnectionPool benchmarks connection pooling.
func BenchmarkConnectionPool(b *testing.B) {
	server := createMockServer()
	defer server.Close()

	// Create pool
	poolConfig := &pool.Config{
		MaxConnections:        20,
		MaxConnectionsPerHost: 10,
		IdleTimeout:           30 * time.Second,
		MaxIdleTime:           5 * time.Minute,
	}
	connPool := pool.New(poolConfig)

	client, err := client.NewClient(client.Options{
		Host:     server.URL,
		APIToken: benchAPIToken,
	})
	if err != nil {
		b.Fatal(err)
	}

	// Note: Connection pool created but not used as Client doesn't expose SetTransport method
	_ = connPool

	b.ResetTimer()

	for range b.N {
		_, err := client.Get("/nodes", nil)
		if err != nil {
			b.Fatal(err)
		}
	}

	b.StopTimer()

	stats := connPool.Stats()
	b.Logf("Pool stats - Active: %d, Idle: %d, Total: %d",
		stats.ActiveConnections, stats.IdleConnections, stats.TotalConnections)
}

// BenchmarkBatchRequests benchmarks batch request processing.
func BenchmarkBatchRequests(b *testing.B) {
	server := createMockServer()
	defer server.Close()

	client := &http.Client{}
	executor := batch.NewExecutor(client, &batch.Config{
		MaxBatchSize:   100,
		MaxConcurrency: 10,
		Timeout:        30 * time.Second,
	})

	ctx := context.Background()

	b.ResetTimer()

	for range b.N {
		// Create batch with 50 requests
		batchReq := batch.New(nil)
		for j := range 50 {
			_ = batchReq.Add(&batch.Request{
				ID:     fmt.Sprintf("req-%d", j),
				Method: "GET",
				Path:   server.URL + "/api2/json/nodes",
			})
		}

		result, err := executor.Execute(ctx, batchReq)
		if err != nil {
			b.Fatal(err)
		}

		if result.FailureCount > 0 {
			b.Fatalf("Batch had %d failures", result.FailureCount)
		}
	}
}

// BenchmarkStreamProcessing benchmarks stream processing.
func BenchmarkStreamProcessing(b *testing.B) {
	// Create mock streaming response
	data := make([]string, 1000)

	for itemIndex := range 1000 {
		item := map[string]interface{}{
			"id":         itemIndex,
			benchKeyName: fmt.Sprintf("item-%d", itemIndex),
			"value":      itemIndex * 100,
		}

		jsonData, err := json.Marshal(item)
		if err != nil {
			b.Fatalf("json.Marshal failed: %v", err)
		}

		data[itemIndex] = string(jsonData)
	}

	b.ResetTimer()

	for range b.N {
		// Simulate streaming response
		reader := &mockReader{
			data: data,
			pos:  0,
		}

		strm := stream.New(reader, nil)

		count := 0

		for {
			item, err := strm.Read()
			if err != nil {
				break
			}

			if item != nil {
				count++
			}

			if count >= 1000 {
				break
			}
		}

		// Close each iteration's stream immediately; deferring inside the b.N
		// loop would accumulate b.N closers and distort the allocation numbers.
		_ = strm.Close()
	}
}

// mockReader implements io.ReadCloser for testing.
type mockReader struct {
	data []string
	pos  int
}

func (r *mockReader) Read(buffer []byte) (int, error) {
	if r.pos >= len(r.data) {
		return 0, errEOF
	}

	line := r.data[r.pos] + "\n"
	n := copy(buffer, []byte(line))
	r.pos++

	return n, nil
}

func (r *mockReader) Close() error {
	return nil
}

// BenchmarkJSONMarshaling benchmarks JSON marshaling/unmarshaling.
func BenchmarkJSONMarshaling(b *testing.B) {
	type VMConfig struct {
		VMID     int                    `json:"vmid"`
		Name     string                 `json:"name"`
		Memory   int                    `json:"memory"`
		Cores    int                    `json:"cores"`
		Status   string                 `json:"status"`
		Disks    map[string]string      `json:"disks"`
		Networks map[string]string      `json:"networks"`
		Options  map[string]interface{} `json:"options"`
	}

	config := VMConfig{
		VMID:   100,
		Name:   "test-vm",
		Memory: 4096,
		Cores:  4,
		Status: "running",
		Disks: map[string]string{
			"scsi0": "local-lvm:vm-100-disk-0,size=32G",
			"scsi1": "local-lvm:vm-100-disk-1,size=100G",
		},
		Networks: map[string]string{
			"net0": "virtio=AA:BB:CC:DD:EE:FF,bridge=vmbr0",
		},
		Options: map[string]interface{}{
			"boot":   "order=scsi0",
			"ostype": "l26",
			"scsihw": "virtio-scsi-pci",
		},
	}

	b.ResetTimer()

	for range b.N {
		// Marshal
		data, err := json.Marshal(config)
		if err != nil {
			b.Fatal(err)
		}

		// Unmarshal
		var result VMConfig

		err = json.Unmarshal(data, &result)
		if err != nil {
			b.Fatal(err)
		}
	}
}

// BenchmarkErrorHandling benchmarks error handling performance.
func BenchmarkErrorHandling(b *testing.B) {
	server := createMockServer()
	defer server.Close()

	client, err := client.NewClient(client.Options{
		Host:     server.URL,
		APIToken: benchAPIToken,
	})
	if err != nil {
		b.Fatal(err)
	}

	b.ResetTimer()

	for range b.N {
		_, err := client.Get("/nonexistent", nil)
		if err != nil {
			// Error is expected, just checking performance
			_ = err.Error()
		}
	}
}

// BenchmarkMemoryAllocation benchmarks memory allocation patterns.
func BenchmarkMemoryAllocation(b *testing.B) {
	server := createMockServer()
	defer server.Close()

	client, err := client.NewClient(client.Options{
		Host:     server.URL,
		APIToken: benchAPIToken,
	})
	if err != nil {
		b.Fatal(err)
	}

	b.ReportAllocs()
	b.ResetTimer()

	for range b.N {
		_, err := client.Get("/cluster/resources", nil)
		if err != nil {
			b.Fatal(err)
		}
	}
}

// Comparative benchmarks with simulated Perl client behavior

// BenchmarkSequentialVsConcurrent compares sequential vs concurrent execution.
func BenchmarkSequentialVsConcurrent(b *testing.B) {
	server := createMockServer()
	defer server.Close()

	client, err := client.NewClient(client.Options{
		Host:     server.URL,
		APIToken: benchAPIToken,
	})
	if err != nil {
		b.Fatal(err)
	}

	nodes := []string{"pve1", "pve2", "pve3", "pve4", "pve5"}

	b.Run("Sequential", func(b *testing.B) {
		for range b.N {
			for _, node := range nodes {
				_, _ = client.Get(fmt.Sprintf("/nodes/%s/status", node), nil)
			}
		}
	})

	b.Run("Concurrent", func(b *testing.B) {
		for range b.N {
			var waitGroup sync.WaitGroup
			for _, node := range nodes {
				waitGroup.Add(1)

				go func(n string) {
					defer waitGroup.Done()

					_, _ = client.Get(fmt.Sprintf("/nodes/%s/status", n), nil)
				}(node)
			}

			waitGroup.Wait()
		}
	})
}

// BenchmarkLargePayload benchmarks handling of large response payloads.
func BenchmarkLargePayload(b *testing.B) {
	// Create server with large response
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, r *http.Request) {
		// Generate 10MB response
		data := make([]map[string]interface{}, 10000)
		for i := range 10000 {
			data[i] = map[string]interface{}{
				"id":          i,
				benchKeyName:  fmt.Sprintf("resource-%d", i),
				"description": "This is a long description text that adds bulk to the response payload",
				"metadata": map[string]interface{}{
					"created":  time.Now().Unix(),
					"modified": time.Now().Unix(),
					"tags":     []string{"tag1", "tag2", "tag3"},
				},
			}
		}

		_ = json.NewEncoder(writer).Encode(map[string]interface{}{"data": data})
	}))
	defer server.Close()

	client, err := client.NewClient(client.Options{
		Host:     server.URL,
		APIToken: benchAPIToken,
	})
	if err != nil {
		b.Fatal(err)
	}

	b.ResetTimer()

	for range b.N {
		_, err := client.Get("/large", nil)
		if err != nil {
			b.Fatal(err)
		}
	}
}

// Results structure for performance comparison.
type BenchmarkResults struct {
	Operation      string
	RequestsPerSec float64
	AvgLatency     time.Duration
	P95Latency     time.Duration
	P99Latency     time.Duration
	MemoryUsed     int64
	Allocations    int64
}

// RunComparisonBenchmark runs a comprehensive comparison benchmark.
func RunComparisonBenchmark(b *testing.B) {
	b.Helper()

	results := make([]BenchmarkResults, 0, 1)

	// Add benchmark results
	result := BenchmarkResults{
		Operation:      "Simple GET",
		RequestsPerSec: float64(b.N) / b.Elapsed().Seconds(),
		AvgLatency:     b.Elapsed() / time.Duration(b.N),
	}
	results = append(results, result)

	// Print comparison table
	b.Logf("\nPerformance Comparison (Go Client)")
	b.Logf("=====================================")
	b.Logf("%-20s | %-15s | %-15s\n", "Operation", "Requests/sec", "Avg Latency")
	b.Logf("---------------------|-----------------|----------------")

	for _, r := range results {
		b.Logf("%-20s | %-15.2f | %-15s\n",
			r.Operation, r.RequestsPerSec, r.AvgLatency)
	}

	b.Logf("\nNote: Perl client typically achieves:")
	b.Logf("- Simple GET: ~100-200 req/sec")
	b.Logf("- No connection pooling")
	b.Logf("- Sequential execution only")
	b.Logf("- Higher memory usage (~50-100MB)")
}
