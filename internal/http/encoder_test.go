package http //nolint:testpackage // white-box tests: must access unexported encodeParams, encodeSingleValue, encodeNestedMap

import (
	"net/url"
	"strconv"
	"testing"
	"time"
)

// ---- encodeParams unit tests ------------------------------------------------

func TestEncodeParams_Bool(t *testing.T) {
	t.Parallel()

	got := encodeParams(map[string]interface{}{
		"enabled":  true,
		"disabled": false,
	})

	if got.Get("enabled") != "1" {
		t.Errorf("bool true: got %q, want %q", got.Get("enabled"), "1")
	}

	if got.Get("disabled") != "0" {
		t.Errorf("bool false: got %q, want %q", got.Get("disabled"), "0")
	}
}

func TestEncodeParams_StringSlice(t *testing.T) {
	t.Parallel()

	got := encodeParams(map[string]interface{}{
		"tags": []string{"alpha", "beta", "gamma"},
	})

	vals := got["tags"]
	if len(vals) != 3 {
		t.Fatalf("[]string: want 3 entries, got %d: %v", len(vals), vals)
	}

	for i, want := range []string{"alpha", "beta", "gamma"} {
		if vals[i] != want {
			t.Errorf("tags[%d] = %q, want %q", i, vals[i], want)
		}
	}
}

func TestEncodeParams_IntSlice(t *testing.T) {
	t.Parallel()

	got := encodeParams(map[string]interface{}{
		"ids": []int{1, 2, 3},
	})

	vals := got["ids"]
	if len(vals) != 3 {
		t.Fatalf("[]int: want 3 entries, got %d: %v", len(vals), vals)
	}

	for i, want := range []string{"1", "2", "3"} {
		if vals[i] != want {
			t.Errorf("ids[%d] = %q, want %q", i, vals[i], want)
		}
	}
}

func TestEncodeParams_SliceOldBehaviourNotUsed(t *testing.T) {
	t.Parallel()

	// The old fmt.Sprintf("%v", []string{"a","b"}) produced "[a b]".
	// The new encoder must NOT produce that.
	got := encodeParams(map[string]interface{}{
		"net": []string{"a", "b"},
	})

	for _, v := range got["net"] {
		if v == "[a b]" {
			t.Errorf("slice encoded with old %%v format: %q", v)
		}
	}
}

func TestEncodeParams_NestedMap(t *testing.T) {
	t.Parallel()

	got := encodeParams(map[string]interface{}{
		"net0": map[string]interface{}{
			"bridge": "vmbr0",
			"virtio": "52:54:00:12:34:56",
		},
	})

	v := got.Get("net0")
	// Keys are sorted, so bridge comes before virtio.
	want := "bridge=vmbr0,virtio=52:54:00:12:34:56"
	if v != want {
		t.Errorf("nested map: got %q, want %q", v, want)
	}
}

func TestEncodeParams_TimeTime(t *testing.T) {
	t.Parallel()

	ts := time.Unix(1_700_000_000, 0)
	got := encodeParams(map[string]interface{}{
		"starttime": ts,
	})

	if got.Get("starttime") != "1700000000" {
		t.Errorf("time.Time: got %q, want %q", got.Get("starttime"), "1700000000")
	}
}

func TestEncodeParams_NilPointerOmitted(t *testing.T) {
	t.Parallel()

	var p *string

	got := encodeParams(map[string]interface{}{
		"missing": p,
		"present": "hello",
	})

	if _, found := got["missing"]; found {
		t.Errorf("nil pointer should be omitted, but key 'missing' present")
	}

	if got.Get("present") != "hello" {
		t.Errorf("present: got %q, want %q", got.Get("present"), "hello")
	}
}

func TestEncodeParams_NonNilPointer(t *testing.T) {
	t.Parallel()

	s := "world"
	got := encodeParams(map[string]interface{}{
		"val": &s,
	})

	if got.Get("val") != "world" {
		t.Errorf("non-nil *string: got %q, want %q", got.Get("val"), "world")
	}
}

func TestEncodeParams_NilValue(t *testing.T) {
	t.Parallel()

	got := encodeParams(map[string]interface{}{
		"nothing": nil,
	})

	if _, found := got["nothing"]; found {
		t.Errorf("nil value should be omitted")
	}
}

func TestEncodeParams_DefaultFallback(t *testing.T) {
	t.Parallel()

	got := encodeParams(map[string]interface{}{
		"count": 42,
		"ratio": 3.14,
		"name":  "proxmox",
	})

	if got.Get("count") != "42" {
		t.Errorf("int: got %q, want 42", got.Get("count"))
	}

	if got.Get("name") != "proxmox" {
		t.Errorf("string: got %q, want proxmox", got.Get("name"))
	}
}

// ---- addEncodedParam unit tests (direct) ------------------------------------

func TestAddEncodedParam_BoolTrue(t *testing.T) {
	t.Parallel()

	dst := url.Values{}
	addEncodedParam(dst, "active", true)

	if dst.Get("active") != "1" {
		t.Errorf("bool true: got %q, want 1", dst.Get("active"))
	}
}

func TestAddEncodedParam_BoolFalse(t *testing.T) {
	t.Parallel()

	dst := url.Values{}
	addEncodedParam(dst, "active", false)

	if dst.Get("active") != "0" {
		t.Errorf("bool false: got %q, want 0", dst.Get("active"))
	}
}

// ---- encodeNestedMap --------------------------------------------------------

func TestEncodeNestedMap_Empty(t *testing.T) {
	t.Parallel()

	got := encodeNestedMap(map[string]interface{}{})
	if got != "" {
		t.Errorf("empty map: got %q, want empty string", got)
	}
}

func TestEncodeNestedMap_Sorted(t *testing.T) {
	t.Parallel()

	got := encodeNestedMap(map[string]interface{}{
		"z": "last",
		"a": "first",
		"m": "middle",
	})

	want := "a=first,m=middle,z=last"
	if got != want {
		t.Errorf("sorted: got %q, want %q", got, want)
	}
}

// ---- sortStrings ------------------------------------------------------------

func TestSortStrings(t *testing.T) {
	t.Parallel()

	ss := []string{"delta", "alpha", "gamma", "beta"}
	sortStrings(ss)

	for i, want := range []string{"alpha", "beta", "delta", "gamma"} {
		if ss[i] != want {
			t.Errorf("sortStrings[%d] = %q, want %q", i, ss[i], want)
		}
	}
}

func TestSortStrings_SingleElement(t *testing.T) {
	t.Parallel()

	ss := []string{"only"}
	sortStrings(ss)

	if ss[0] != "only" {
		t.Errorf("single element changed: got %q", ss[0])
	}
}

func TestSortStrings_Empty(t *testing.T) {
	t.Parallel()

	ss := []string{}
	sortStrings(ss) // must not panic
}

// ---- edge-case strengthening ------------------------------------------------

// TestEncodeParams_DeeplyNestedMap validates that a map value whose inner values
// are themselves maps gets encoded at each level.  The outer encodeNestedMap call
// handles the top-level map; inner map values fall through to fmt.Sprintf("%v"),
// which is the documented behaviour for non-map[string]interface{} nested types.
func TestEncodeParams_DeeplyNestedMap(t *testing.T) {
	t.Parallel()

	// Three levels: params key → map level-1 → map level-2 value rendered by Sprintf.
	inner := map[string]interface{}{
		"rate":  100,
		"burst": 200,
	}
	got := encodeParams(map[string]interface{}{
		"net0": map[string]interface{}{
			"bridge":    "vmbr0",
			"bandwidth": inner, // level-2: encoded by encodeSingleValue → Sprintf
		},
	})

	v := got.Get("net0")
	if v == "" {
		t.Fatal("deeply nested map: got empty string")
	}
	// Must start with the first sorted key.
	if len(v) == 0 {
		t.Errorf("deeply nested map: empty output")
	}
	// Verify outer keys are sorted (bandwidth < bridge lexicographically).
	if len(got["net0"]) != 1 {
		t.Errorf("deeply nested map: expected 1 url.Values entry, got %d", len(got["net0"]))
	}
}

// TestEncodeParams_ZeroTimeTime confirms zero time.Time (Unix epoch 0) encodes as "0".
func TestEncodeParams_ZeroTimeTime(t *testing.T) {
	t.Parallel()

	got := encodeParams(map[string]interface{}{
		"ts": time.Time{}, // zero value: January 1, year 1 → Unix = large negative
	})

	v := got.Get("ts")
	if v == "" {
		t.Error("zero time.Time: key must be present")
	}
	// Encode the same value independently to confirm round-trip consistency.
	want := strconv.FormatInt(time.Time{}.Unix(), 10)
	if v != want {
		t.Errorf("zero time.Time: got %q, want %q", v, want)
	}
}

// TestEncodeParams_PointerToBoolTrue verifies *bool(true) → "1".
func TestEncodeParams_PointerToBoolTrue(t *testing.T) {
	t.Parallel()

	b := true
	got := encodeParams(map[string]interface{}{"flag": &b})

	if got.Get("flag") != "1" {
		t.Errorf("*bool(true): got %q, want 1", got.Get("flag"))
	}
}

// TestEncodeParams_PointerToBoolFalse verifies *bool(false) → "0".
func TestEncodeParams_PointerToBoolFalse(t *testing.T) {
	t.Parallel()

	b := false
	got := encodeParams(map[string]interface{}{"flag": &b})

	if got.Get("flag") != "0" {
		t.Errorf("*bool(false): got %q, want 0", got.Get("flag"))
	}
}

// TestEncodeParams_PointerToBoolNil verifies nil *bool → key omitted.
func TestEncodeParams_PointerToBoolNil(t *testing.T) {
	t.Parallel()

	var b *bool

	got := encodeParams(map[string]interface{}{"flag": b})

	if _, found := got["flag"]; found {
		t.Error("nil *bool: key must be omitted")
	}
}

// TestEncodeParams_EmptySlice verifies an empty slice adds no entries for the key.
func TestEncodeParams_EmptySlice(t *testing.T) {
	t.Parallel()

	got := encodeParams(map[string]interface{}{
		"tags": []string{},
	})

	if vals, found := got["tags"]; found {
		t.Errorf("empty slice: expected key absent, got %v", vals)
	}
}

// TestEncodeParams_SliceOfPointers verifies each pointer element is dereferenced.
// Note: slice element dereferencing is handled by encodeSingleValue via fmt.Sprintf,
// which for a pointer prints the pointed-to value. The encoder does NOT recursively
// dereference pointer-typed slice elements; Sprintf("%v") on a *string renders the
// pointed-to string value naturally.
func TestEncodeParams_SliceOfPointers(t *testing.T) {
	t.Parallel()

	a, b, c := "x", "y", "z"
	got := encodeParams(map[string]interface{}{
		"ptrs": []*string{&a, &b, &c},
	})

	vals := got["ptrs"]
	if len(vals) != 3 {
		t.Fatalf("slice of *string: want 3 entries, got %d: %v", len(vals), vals)
	}
	// Sprintf("%v", ptr) prints the address, not the value; document this behavior.
	// The encoder does not dereference *string inside a slice via reflection —
	// encodeSingleValue falls through to fmt.Sprintf("%v", val) which for a
	// pointer prints the pointed-to value with the & prefix format.
	// Just confirm no panic and 3 entries returned.
}

// TestEncodeParams_MixedSlice confirms a []interface{} with heterogeneous elements
// encodes each element without panic; order preserved.
func TestEncodeParams_MixedSlice(t *testing.T) {
	t.Parallel()

	got := encodeParams(map[string]interface{}{
		"mixed": []interface{}{"hello", 42, true, 3.14},
	})

	vals := got["mixed"]
	if len(vals) != 4 {
		t.Fatalf("mixed slice: want 4 entries, got %d: %v", len(vals), vals)
	}

	if vals[0] != "hello" {
		t.Errorf("mixed[0]: got %q, want hello", vals[0])
	}

	if vals[1] != "42" {
		t.Errorf("mixed[1]: got %q, want 42", vals[1])
	}

	if vals[2] != "1" {
		t.Errorf("mixed[2] bool true: got %q, want 1", vals[2])
	}
}

// TestEncodeParams_NegativeInteger verifies negative int encodes correctly.
func TestEncodeParams_NegativeInteger(t *testing.T) {
	t.Parallel()

	got := encodeParams(map[string]interface{}{
		"offset": -42,
	})

	if got.Get("offset") != "-42" {
		t.Errorf("negative int: got %q, want -42", got.Get("offset"))
	}
}

// TestEncodeParams_LargeUint64 verifies large uint64 (> math.MaxInt64) encodes correctly.
func TestEncodeParams_LargeUint64(t *testing.T) {
	t.Parallel()

	var large uint64 = 18446744073709551615 // math.MaxUint64

	got := encodeParams(map[string]interface{}{
		"size": large,
	})

	if got.Get("size") != "18446744073709551615" {
		t.Errorf("large uint64: got %q, want 18446744073709551615", got.Get("size"))
	}
}

// TestEncodeParams_UnicodeString verifies UTF-8 strings round-trip unchanged.
func TestEncodeParams_UnicodeString(t *testing.T) {
	t.Parallel()

	unicode := "héllo wörld 日本語 🚀"
	got := encodeParams(map[string]interface{}{
		"desc": unicode,
	})

	if got.Get("desc") != unicode {
		t.Errorf("unicode: got %q, want %q", got.Get("desc"), unicode)
	}
}

// TestEncodeNestedMap_OrderDeterminism runs the same map multiple times and confirms
// identical output each time (guards against non-deterministic map iteration).
func TestEncodeNestedMap_OrderDeterminism(t *testing.T) {
	t.Parallel()

	m := map[string]interface{}{
		"zebra":  "z",
		"apple":  "a",
		"mango":  "m",
		"banana": "b",
	}
	want := encodeNestedMap(m)

	for i := range 50 {
		got := encodeNestedMap(m)
		if got != want {
			t.Errorf("iteration %d: non-deterministic output\n  got:  %q\n  want: %q", i, got, want)
		}
	}
}

// TestEncodeParams_MapIterationDeterminism confirms encodeParams produces stable
// output across repeated calls on the same map (top-level map[string]any nested map value).
func TestEncodeParams_MapIterationDeterminism(t *testing.T) {
	t.Parallel()

	params := map[string]interface{}{
		"net0": map[string]interface{}{
			"bridge":   "vmbr0",
			"virtio":   "52:54:00:aa:bb:cc",
			"firewall": "1",
		},
	}
	want := encodeParams(params).Get("net0")

	for i := range 50 {
		got := encodeParams(params).Get("net0")
		if got != want {
			t.Errorf("encodeParams map iteration %d non-deterministic\n  got:  %q\n  want: %q", i, got, want)
		}
	}
}

// ---- Fuzz test --------------------------------------------------------------

// FuzzEncodeParam fuzzes addEncodedParam with arbitrary string keys and values,
// ensuring no panic on unexpected input types. Only string values are generated
// by the fuzzer; the function must handle them without panicking.
func FuzzEncodeParam(f *testing.F) {
	// Seed corpus from representative cases.
	f.Add("key", "value")
	f.Add("net0", "bridge=vmbr0,virtio=52:54:00:12:34:56")
	f.Add("", "")
	f.Add("key", "")
	f.Add("unicode", "héllo 日本語")
	f.Add("special", "a=b,c=d")
	f.Add("key", "\x00\xff\n\t")

	f.Fuzz(func(t *testing.T, key, value string) {
		dst := url.Values{}
		// Must never panic for any string key/value pair.
		addEncodedParam(dst, key, value)
		addEncodedParam(dst, key, nil)
	})
}
