package metrics_test

import (
	"bytes"
	"strings"
	"testing"
	"time"

	"github.com/fivetwenty-io/proxmox-apiclient-go/v3/pkg/metrics"
)

// ---- Counter ----

func TestCounter_IncAndGet(t *testing.T) {
	t.Parallel()

	coll := metrics.NewCollector("test")

	counter := coll.NewCounter("hits", "total hits")
	if counter.Get() != 0 {
		t.Fatalf("initial value: want 0, got %d", counter.Get())
	}

	counter.Inc()
	counter.Inc()

	if counter.Get() != 2 {
		t.Fatalf("after 2 Inc: want 2, got %d", counter.Get())
	}
}

func TestCounter_Add(t *testing.T) {
	t.Parallel()

	coll := metrics.NewCollector("test")
	counter := coll.NewCounter("bytes", "bytes sent")
	counter.Add(100)
	counter.Add(50)

	if counter.Get() != 150 {
		t.Errorf("Add: want 150, got %d", counter.Get())
	}
}

func TestCounter_Idempotent(t *testing.T) {
	t.Parallel()

	coll := metrics.NewCollector("test")
	counter1 := coll.NewCounter("same", "same counter")
	counter2 := coll.NewCounter("same", "same counter")

	counter1.Inc()
	// Both references point to same metric; counter2.Get() may or may not reflect counter1's Inc
	// (impl returns existing counter on second call).
	if counter2.Get() != 1 {
		t.Errorf("idempotent counter: want 1, got %d", counter2.Get())
	}
}

// ---- Gauge ----

func TestGauge_SetIncDec(t *testing.T) {
	t.Parallel()

	coll := metrics.NewCollector("test")
	gauge := coll.NewGauge("conn", "connections")
	gauge.Set(10)

	if gauge.Get() != 10 {
		t.Errorf("Set: want 10, got %d", gauge.Get())
	}

	gauge.Inc()

	if gauge.Get() != 11 {
		t.Errorf("Inc: want 11, got %d", gauge.Get())
	}

	gauge.Dec()

	if gauge.Get() != 10 {
		t.Errorf("Dec: want 10, got %d", gauge.Get())
	}
}

func TestGauge_Add(t *testing.T) {
	t.Parallel()

	coll := metrics.NewCollector("test")
	gauge := coll.NewGauge("mem", "memory bytes")
	gauge.Add(1024)
	gauge.Add(-512)

	if gauge.Get() != 512 {
		t.Errorf("Add: want 512, got %d", gauge.Get())
	}
}

func TestGauge_Idempotent(t *testing.T) {
	t.Parallel()

	coll := metrics.NewCollector("test")
	gauge1 := coll.NewGauge("same_g", "same gauge")
	gauge2 := coll.NewGauge("same_g", "same gauge")

	gauge1.Set(99)

	if gauge2.Get() != 99 {
		t.Errorf("idempotent gauge: want 99, got %d", gauge2.Get())
	}
}

// ---- Histogram ----

func TestHistogram_ObserveAndGetStats(t *testing.T) {
	t.Parallel()

	coll := metrics.NewCollector("test")
	hist := coll.NewHistogram("duration", "request duration", []float64{0.1, 0.5, 1.0})
	hist.Observe(0.05)
	hist.Observe(0.3)
	hist.Observe(0.8)

	count, sum, buckets := hist.GetStats()
	if count != 3 {
		t.Errorf("count: want 3, got %d", count)
	}

	if sum <= 0 {
		t.Errorf("sum: want >0, got %f", sum)
	}

	if len(buckets) != 3 {
		t.Errorf("buckets: want 3 entries, got %d", len(buckets))
	}
}

func TestHistogram_DefaultBuckets(t *testing.T) {
	t.Parallel()

	coll := metrics.NewCollector("test")
	// nil buckets → default set applied.
	hist := coll.NewHistogram("default_hist", "default buckets", nil)
	hist.Observe(0.01)

	count, _, _ := hist.GetStats()
	if count != 1 {
		t.Errorf("default buckets count: want 1, got %d", count)
	}
}

func TestHistogram_Idempotent(t *testing.T) {
	t.Parallel()

	coll := metrics.NewCollector("test")
	hist1 := coll.NewHistogram("same_h", "same", nil)
	hist2 := coll.NewHistogram("same_h", "same", nil)

	hist1.Observe(1.0)

	cnt, _, _ := hist2.GetStats()
	if cnt != 1 {
		t.Errorf("idempotent histogram: want 1, got %d", cnt)
	}
}

// ---- Summary ----

func TestSummary_ObserveAndGetQuantiles(t *testing.T) {
	t.Parallel()

	coll := metrics.NewCollector("test")
	summary := coll.NewSummary("latency", "request latency", time.Minute)

	for i := range 100 {
		summary.Observe(float64(i))
	}

	quantiles := summary.GetQuantiles([]float64{0.5, 0.9, 0.99})
	if len(quantiles) != 3 {
		t.Fatalf("quantiles: want 3 entries, got %d", len(quantiles))
	}

	if quantiles[0.5] <= 0 {
		t.Errorf("p50: want >0, got %f", quantiles[0.5])
	}

	if quantiles[0.99] < quantiles[0.5] {
		t.Errorf("p99 (%f) < p50 (%f)", quantiles[0.99], quantiles[0.5])
	}
}

func TestSummary_EmptyQuantiles(t *testing.T) {
	t.Parallel()

	coll := metrics.NewCollector("test")
	summary := coll.NewSummary("empty_s", "empty", time.Minute)

	quantiles := summary.GetQuantiles([]float64{0.5, 0.99})
	for _, v := range quantiles {
		if v != 0 {
			t.Errorf("empty summary quantile: want 0, got %f", v)
		}
	}
}

func TestSummary_DefaultMaxAge(t *testing.T) {
	t.Parallel()

	coll := metrics.NewCollector("test")
	// maxAge=0 → default applied; must not panic.
	summary := coll.NewSummary("defage", "default max age", 0)
	summary.Observe(1.0)

	quantiles := summary.GetQuantiles([]float64{0.5})
	if quantiles[0.5] == 0 {
		t.Errorf("default maxAge summary: want >0 for p50")
	}
}

func TestSummary_Idempotent(t *testing.T) {
	t.Parallel()

	coll := metrics.NewCollector("test")
	summary1 := coll.NewSummary("same_s", "same", time.Minute)
	summary2 := coll.NewSummary("same_s", "same", time.Minute)

	summary1.Observe(7.0)

	quantiles := summary2.GetQuantiles([]float64{0.5})
	if quantiles[0.5] == 0 {
		t.Errorf("idempotent summary: want non-zero p50")
	}
}

// ---- SetLabels / Export ----

func TestCollector_SetLabels(t *testing.T) {
	t.Parallel()

	coll := metrics.NewCollector("test")
	coll.SetLabels(map[string]string{"env": "test", "region": "us-east"})
	counter := coll.NewCounter("labeled", "labeled counter")
	counter.Inc()

	var buf bytes.Buffer

	err := coll.Export(&buf)
	if err != nil {
		t.Fatalf("Export: %v", err)
	}

	out := buf.String()
	if !strings.Contains(out, "env=") {
		t.Errorf("SetLabels: want env label in export, got %q", out)
	}
}

func TestCollector_Export_Counters(t *testing.T) {
	t.Parallel()

	coll := metrics.NewCollector("myapp")
	counter := coll.NewCounter("reqs", "total requests")
	counter.Add(42)

	var buf bytes.Buffer

	err := coll.Export(&buf)
	if err != nil {
		t.Fatalf("Export: %v", err)
	}

	out := buf.String()
	if !strings.Contains(out, "# HELP myapp_reqs") {
		t.Errorf("Export: want HELP line, got %q", out)
	}

	if !strings.Contains(out, "# TYPE myapp_reqs counter") {
		t.Errorf("Export: want TYPE line, got %q", out)
	}

	if !strings.Contains(out, "42") {
		t.Errorf("Export: want counter value 42, got %q", out)
	}
}

func TestCollector_Export_Gauges(t *testing.T) {
	t.Parallel()

	coll := metrics.NewCollector("myapp")
	gauge := coll.NewGauge("active", "active conns")
	gauge.Set(5)

	var buf bytes.Buffer

	err := coll.Export(&buf)
	if err != nil {
		t.Fatalf("Export: %v", err)
	}

	out := buf.String()
	if !strings.Contains(out, "# TYPE myapp_active gauge") {
		t.Errorf("Export: want gauge TYPE, got %q", out)
	}

	if !strings.Contains(out, "5") {
		t.Errorf("Export: want gauge value 5, got %q", out)
	}
}

func TestCollector_Export_Histograms(t *testing.T) {
	t.Parallel()

	coll := metrics.NewCollector("myapp")
	hist := coll.NewHistogram("dur", "duration", []float64{0.1, 0.5})
	hist.Observe(0.05)
	hist.Observe(0.4)

	var buf bytes.Buffer

	err := coll.Export(&buf)
	if err != nil {
		t.Fatalf("Export: %v", err)
	}

	out := buf.String()
	if !strings.Contains(out, "# TYPE myapp_dur histogram") {
		t.Errorf("Export: want histogram TYPE, got %q", out)
	}

	if !strings.Contains(out, "_bucket") {
		t.Errorf("Export: want bucket lines, got %q", out)
	}

	if !strings.Contains(out, "+Inf") {
		t.Errorf("Export: want +Inf bucket, got %q", out)
	}
}

func TestCollector_Export_Summaries(t *testing.T) {
	t.Parallel()

	coll := metrics.NewCollector("myapp")
	summary := coll.NewSummary("lat", "latency", time.Minute)
	summary.Observe(0.2)
	summary.Observe(0.5)

	var buf bytes.Buffer

	err := coll.Export(&buf)
	if err != nil {
		t.Fatalf("Export: %v", err)
	}

	out := buf.String()
	if !strings.Contains(out, "# TYPE myapp_lat summary") {
		t.Errorf("Export: want summary TYPE, got %q", out)
	}

	if !strings.Contains(out, "quantile=") {
		t.Errorf("Export: want quantile labels, got %q", out)
	}
}

func TestCollector_Reset(t *testing.T) {
	t.Parallel()

	coll := metrics.NewCollector("test")
	counter := coll.NewCounter("r", "reset")
	gauge := coll.NewGauge("rg", "reset gauge")
	hist := coll.NewHistogram("rh", "reset hist", nil)
	summary := coll.NewSummary("rs", "reset summary", time.Minute)

	counter.Add(10)
	gauge.Set(5)
	hist.Observe(1.0)
	summary.Observe(1.0)

	coll.Reset()

	if counter.Get() != 0 {
		t.Errorf("Reset counter: want 0, got %d", counter.Get())
	}

	if gauge.Get() != 0 {
		t.Errorf("Reset gauge: want 0, got %d", gauge.Get())
	}

	cnt, _, _ := hist.GetStats()
	if cnt != 0 {
		t.Errorf("Reset histogram: want count 0, got %d", cnt)
	}
}

// ---- DefaultMetrics ----

func TestNewDefaultMetrics(t *testing.T) {
	t.Parallel()

	defaultMetrics := metrics.NewDefaultMetrics()
	if defaultMetrics == nil {
		t.Fatal("NewDefaultMetrics returned nil")
	}

	defaultMetrics.RequestsTotal.Inc()
	defaultMetrics.RequestsFailedTotal.Add(2)
	defaultMetrics.RequestDuration.Observe(0.1)
	defaultMetrics.ActiveConnections.Set(3)
	defaultMetrics.BytesSent.Add(1024)
	defaultMetrics.BytesReceived.Add(512)

	var buf bytes.Buffer

	err := defaultMetrics.Export(&buf)
	if err != nil {
		t.Fatalf("DefaultMetrics.Export: %v", err)
	}

	out := buf.String()
	if !strings.Contains(out, "pve_api_client_requests_total") {
		t.Errorf("DefaultMetrics: missing requests_total, got %q", out)
	}
}

func TestDefaultMetrics_Reset(t *testing.T) {
	t.Parallel()

	defaultMetrics := metrics.NewDefaultMetrics()
	defaultMetrics.RequestsTotal.Add(99)
	defaultMetrics.Reset()

	if defaultMetrics.RequestsTotal.Get() != 0 {
		t.Errorf("DefaultMetrics.Reset: want 0, got %d", defaultMetrics.RequestsTotal.Get())
	}
}
