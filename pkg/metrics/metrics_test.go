package metrics_test

import (
	"bytes"
	"strings"
	"testing"
	"time"

	"github.com/fivetwenty-io/pve-apiclient-go/v3/pkg/metrics"
)

// ---- Counter ----

func TestCounter_IncAndGet(t *testing.T) {
	t.Parallel()

	c := metrics.NewCollector("test")

	co := c.NewCounter("hits", "total hits")
	if co.Get() != 0 {
		t.Fatalf("initial value: want 0, got %d", co.Get())
	}

	co.Inc()
	co.Inc()

	if co.Get() != 2 {
		t.Fatalf("after 2 Inc: want 2, got %d", co.Get())
	}
}

func TestCounter_Add(t *testing.T) {
	t.Parallel()

	c := metrics.NewCollector("test")
	co := c.NewCounter("bytes", "bytes sent")
	co.Add(100)
	co.Add(50)

	if co.Get() != 150 {
		t.Errorf("Add: want 150, got %d", co.Get())
	}
}

func TestCounter_Idempotent(t *testing.T) {
	t.Parallel()

	c := metrics.NewCollector("test")
	co1 := c.NewCounter("same", "same counter")
	co2 := c.NewCounter("same", "same counter")

	co1.Inc()
	// Both references point to same metric; co2.Get() may or may not reflect co1's Inc
	// (impl returns existing counter on second call).
	if co2.Get() != 1 {
		t.Errorf("idempotent counter: want 1, got %d", co2.Get())
	}
}

// ---- Gauge ----

func TestGauge_SetIncDec(t *testing.T) {
	t.Parallel()

	c := metrics.NewCollector("test")
	g := c.NewGauge("conn", "connections")
	g.Set(10)

	if g.Get() != 10 {
		t.Errorf("Set: want 10, got %d", g.Get())
	}

	g.Inc()

	if g.Get() != 11 {
		t.Errorf("Inc: want 11, got %d", g.Get())
	}

	g.Dec()

	if g.Get() != 10 {
		t.Errorf("Dec: want 10, got %d", g.Get())
	}
}

func TestGauge_Add(t *testing.T) {
	t.Parallel()

	c := metrics.NewCollector("test")
	g := c.NewGauge("mem", "memory bytes")
	g.Add(1024)
	g.Add(-512)

	if g.Get() != 512 {
		t.Errorf("Add: want 512, got %d", g.Get())
	}
}

func TestGauge_Idempotent(t *testing.T) {
	t.Parallel()

	c := metrics.NewCollector("test")
	g1 := c.NewGauge("same_g", "same gauge")
	g2 := c.NewGauge("same_g", "same gauge")

	g1.Set(99)

	if g2.Get() != 99 {
		t.Errorf("idempotent gauge: want 99, got %d", g2.Get())
	}
}

// ---- Histogram ----

func TestHistogram_ObserveAndGetStats(t *testing.T) {
	t.Parallel()

	c := metrics.NewCollector("test")
	h := c.NewHistogram("duration", "request duration", []float64{0.1, 0.5, 1.0})
	h.Observe(0.05)
	h.Observe(0.3)
	h.Observe(0.8)

	count, sum, buckets := h.GetStats()
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

	c := metrics.NewCollector("test")
	// nil buckets → default set applied.
	h := c.NewHistogram("default_hist", "default buckets", nil)
	h.Observe(0.01)

	count, _, _ := h.GetStats()
	if count != 1 {
		t.Errorf("default buckets count: want 1, got %d", count)
	}
}

func TestHistogram_Idempotent(t *testing.T) {
	t.Parallel()

	c := metrics.NewCollector("test")
	h1 := c.NewHistogram("same_h", "same", nil)
	h2 := c.NewHistogram("same_h", "same", nil)

	h1.Observe(1.0)

	cnt, _, _ := h2.GetStats()
	if cnt != 1 {
		t.Errorf("idempotent histogram: want 1, got %d", cnt)
	}
}

// ---- Summary ----

func TestSummary_ObserveAndGetQuantiles(t *testing.T) {
	t.Parallel()

	c := metrics.NewCollector("test")
	s := c.NewSummary("latency", "request latency", time.Minute)

	for i := range 100 {
		s.Observe(float64(i))
	}

	q := s.GetQuantiles([]float64{0.5, 0.9, 0.99})
	if len(q) != 3 {
		t.Fatalf("quantiles: want 3 entries, got %d", len(q))
	}

	if q[0.5] <= 0 {
		t.Errorf("p50: want >0, got %f", q[0.5])
	}

	if q[0.99] < q[0.5] {
		t.Errorf("p99 (%f) < p50 (%f)", q[0.99], q[0.5])
	}
}

func TestSummary_EmptyQuantiles(t *testing.T) {
	t.Parallel()

	c := metrics.NewCollector("test")
	s := c.NewSummary("empty_s", "empty", time.Minute)

	q := s.GetQuantiles([]float64{0.5, 0.99})
	for _, v := range q {
		if v != 0 {
			t.Errorf("empty summary quantile: want 0, got %f", v)
		}
	}
}

func TestSummary_DefaultMaxAge(t *testing.T) {
	t.Parallel()

	c := metrics.NewCollector("test")
	// maxAge=0 → default applied; must not panic.
	s := c.NewSummary("defage", "default max age", 0)
	s.Observe(1.0)

	q := s.GetQuantiles([]float64{0.5})
	if q[0.5] == 0 {
		t.Errorf("default maxAge summary: want >0 for p50")
	}
}

func TestSummary_Idempotent(t *testing.T) {
	t.Parallel()

	c := metrics.NewCollector("test")
	s1 := c.NewSummary("same_s", "same", time.Minute)
	s2 := c.NewSummary("same_s", "same", time.Minute)

	s1.Observe(7.0)

	q := s2.GetQuantiles([]float64{0.5})
	if q[0.5] == 0 {
		t.Errorf("idempotent summary: want non-zero p50")
	}
}

// ---- SetLabels / Export ----

func TestCollector_SetLabels(t *testing.T) {
	t.Parallel()

	c := metrics.NewCollector("test")
	c.SetLabels(map[string]string{"env": "test", "region": "us-east"})
	co := c.NewCounter("labeled", "labeled counter")
	co.Inc()

	var buf bytes.Buffer
	err := c.Export(&buf)
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

	c := metrics.NewCollector("myapp")
	co := c.NewCounter("reqs", "total requests")
	co.Add(42)

	var buf bytes.Buffer
	err := c.Export(&buf)
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

	c := metrics.NewCollector("myapp")
	g := c.NewGauge("active", "active conns")
	g.Set(5)

	var buf bytes.Buffer
	err := c.Export(&buf)
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

	c := metrics.NewCollector("myapp")
	h := c.NewHistogram("dur", "duration", []float64{0.1, 0.5})
	h.Observe(0.05)
	h.Observe(0.4)

	var buf bytes.Buffer
	err := c.Export(&buf)
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

	c := metrics.NewCollector("myapp")
	s := c.NewSummary("lat", "latency", time.Minute)
	s.Observe(0.2)
	s.Observe(0.5)

	var buf bytes.Buffer
	err := c.Export(&buf)
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

	c := metrics.NewCollector("test")
	co := c.NewCounter("r", "reset")
	g := c.NewGauge("rg", "reset gauge")
	h := c.NewHistogram("rh", "reset hist", nil)
	s := c.NewSummary("rs", "reset summary", time.Minute)

	co.Add(10)
	g.Set(5)
	h.Observe(1.0)
	s.Observe(1.0)

	c.Reset()

	if co.Get() != 0 {
		t.Errorf("Reset counter: want 0, got %d", co.Get())
	}

	if g.Get() != 0 {
		t.Errorf("Reset gauge: want 0, got %d", g.Get())
	}

	cnt, _, _ := h.GetStats()
	if cnt != 0 {
		t.Errorf("Reset histogram: want count 0, got %d", cnt)
	}
}

// ---- DefaultMetrics ----

func TestNewDefaultMetrics(t *testing.T) {
	t.Parallel()

	m := metrics.NewDefaultMetrics()
	if m == nil {
		t.Fatal("NewDefaultMetrics returned nil")
	}

	m.RequestsTotal.Inc()
	m.RequestsFailedTotal.Add(2)
	m.RequestDuration.Observe(0.1)
	m.ActiveConnections.Set(3)
	m.BytesSent.Add(1024)
	m.BytesReceived.Add(512)

	var buf bytes.Buffer
	err := m.Export(&buf)
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

	m := metrics.NewDefaultMetrics()
	m.RequestsTotal.Add(99)
	m.Reset()

	if m.RequestsTotal.Get() != 0 {
		t.Errorf("DefaultMetrics.Reset: want 0, got %d", m.RequestsTotal.Get())
	}
}
