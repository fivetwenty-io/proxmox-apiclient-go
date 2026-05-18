package tasks

import (
	"testing"
)

// TestApplyJitter_ZeroJitterPct: zero jitter returns input unchanged.
func TestApplyJitter_ZeroJitterPct(t *testing.T) {
	t.Parallel()

	got := applyJitter(1000, 0)
	if got != 1000 {
		t.Errorf("jitterPct=0: want 1000, got %d", got)
	}
}

// TestApplyJitter_NegativeJitterPct: negative jitter treated as zero.
func TestApplyJitter_NegativeJitterPct(t *testing.T) {
	t.Parallel()

	got := applyJitter(500, -5)
	if got != 500 {
		t.Errorf("jitterPct<0: want 500, got %d", got)
	}
}

// TestApplyJitter_ZeroMilliseconds: zero ms returns zero regardless of jitterPct.
func TestApplyJitter_ZeroMilliseconds(t *testing.T) {
	t.Parallel()

	got := applyJitter(0, 10)
	if got != 0 {
		t.Errorf("ms=0: want 0, got %d", got)
	}
}

// TestApplyJitter_WindowBounds: result stays within [ms-delta, ms+delta] over many calls.
func TestApplyJitter_WindowBounds(t *testing.T) {
	t.Parallel()

	const (
		milliseconds = 1000
		jitterPct    = 10 // delta = (1000*10)/100 = 100
		iterations   = 200
	)

	delta := (milliseconds * jitterPct) / 100
	loBound := milliseconds - delta
	hiBound := milliseconds + delta

	for range iterations {
		got := applyJitter(milliseconds, jitterPct)
		if got < loBound || got > hiBound {
			t.Errorf("applyJitter(%d,%d)=%d out of [%d,%d]", milliseconds, jitterPct, got, loBound, hiBound)
		}
	}
}

// TestApplyJitter_AtLeastOne: result is never < 1 even with 100% jitter on small input.
func TestApplyJitter_AtLeastOne(t *testing.T) {
	t.Parallel()

	// ms=1, jitterPct=100 => delta=(1*100)/100=1; range [-1,+1]+1 => [0,2], clamped to [1,2]
	for range 100 {
		got := applyJitter(1, 100)
		if got < 1 {
			t.Errorf("result %d < 1", got)
		}
	}
}

// TestApplyJitter_SmallDeltaZeroCase: when computed delta==0 (ms*pct<100), returns ms unchanged.
func TestApplyJitter_SmallDeltaZeroCase(t *testing.T) {
	t.Parallel()

	// ms=1, jitterPct=1 => delta=(1*1)/100=0 => returns ms unchanged
	got := applyJitter(1, 1)
	if got != 1 {
		t.Errorf("delta==0 case: want 1, got %d", got)
	}
}

// TestApplyJitter_LargeJitter: 50% jitter on 200ms; delta=100; range [100,300].
func TestApplyJitter_LargeJitter(t *testing.T) {
	t.Parallel()

	const (
		milliseconds = 200
		jitterPct    = 50
		iterations   = 200
	)

	delta := (milliseconds * jitterPct) / 100
	loBound := milliseconds - delta
	hiBound := milliseconds + delta

	for range iterations {
		got := applyJitter(milliseconds, jitterPct)
		if got < loBound || got > hiBound {
			t.Errorf("got %d out of [%d,%d]", got, loBound, hiBound)
		}
	}
}

// updateInterval tests via taskPoller directly.

func newPoller(backoff bool, maxInterval int) *taskPoller {
	return &taskPoller{
		backoff:     backoff,
		maxInterval: maxInterval,
	}
}

// TestUpdateInterval_NoBackoff: returns cur unchanged when backoff disabled.
func TestUpdateInterval_NoBackoff(t *testing.T) {
	t.Parallel()

	poller := newPoller(false, 5000)
	for _, cur := range []int{100, 500, 1000, 5000, 9999} {
		got := poller.updateInterval(cur)
		if got != cur {
			t.Errorf("noBackoff cur=%d: want %d, got %d", cur, cur, got)
		}
	}
}

// TestUpdateInterval_BackoffBelowMax: doubles interval when below max.
func TestUpdateInterval_BackoffBelowMax(t *testing.T) {
	t.Parallel()

	poller := newPoller(true, 5000)

	got := poller.updateInterval(500) // 500*2=1000 < 5000
	if got != 1000 {
		t.Errorf("backoff below max: want 1000, got %d", got)
	}

	got = poller.updateInterval(2000) // 2000*2=4000 < 5000
	if got != 4000 {
		t.Errorf("backoff 2000->4000: want 4000, got %d", got)
	}
}

// TestUpdateInterval_BackoffCapsAtMax: returns maxInterval when doubled value exceeds it.
func TestUpdateInterval_BackoffCapsAtMax(t *testing.T) {
	t.Parallel()

	poller := newPoller(true, 5000)

	got := poller.updateInterval(3000) // 3000*2=6000 > 5000 => 5000
	if got != 5000 {
		t.Errorf("backoff cap: want 5000, got %d", got)
	}

	got = poller.updateInterval(5000) // 5000*2=10000 > 5000 => 5000
	if got != 5000 {
		t.Errorf("backoff already at max: want 5000, got %d", got)
	}
}

// TestUpdateInterval_BackoffExactMax: doubled value equals max => returns max.
func TestUpdateInterval_BackoffExactMax(t *testing.T) {
	t.Parallel()

	poller := newPoller(true, 4000)

	got := poller.updateInterval(2000) // 2000*2=4000 == max => not > max => returns 4000
	if got != 4000 {
		t.Errorf("backoff exact max: want 4000, got %d", got)
	}
}
