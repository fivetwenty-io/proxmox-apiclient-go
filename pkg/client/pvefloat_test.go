package client_test

import (
	"encoding/json"
	"math"
	"testing"

	"github.com/fivetwenty-io/proxmox-apiclient-go/v3/pkg/client"
)

func TestPVEFloat_Unmarshal(t *testing.T) {
	t.Parallel()

	cases := []struct {
		in   string
		want float64
	}{
		{`1.5`, 1.5},
		{`0`, 0},
		{`-2.25`, -2.25},
		{`42`, 42},
		{`"1.5"`, 1.5},
		{`"0.00"`, 0},
		{`"3.14"`, 3.14},
		{`""`, 0}, // PVE renders an absent number as an empty string
		{`"  2.5 "`, 2.5},
		{`"garbage"`, 0}, // unparseable strings yield 0, never an error
	}

	for _, tc := range cases {
		var f client.PVEFloat

		err := json.Unmarshal([]byte(tc.in), &f)
		if err != nil {
			t.Fatalf("Unmarshal(%s): unexpected error: %v", tc.in, err)
		}

		if f.Float() != tc.want {
			t.Errorf("Unmarshal(%s) = %v, want %v", tc.in, f.Float(), tc.want)
		}
	}
}

func TestPVEFloat_UnmarshalNullLeavesValue(t *testing.T) {
	t.Parallel()

	f := client.PVEFloat(9.0)

	err := json.Unmarshal([]byte(`null`), &f)
	if err != nil {
		t.Fatalf("Unmarshal(null): unexpected error: %v", err)
	}

	if f.Float() != 9.0 {
		t.Errorf("Unmarshal(null) changed value to %v, want 9.0", f.Float())
	}
}

func TestPVEFloat_InStruct(t *testing.T) {
	t.Parallel()

	// A struct mirroring a status payload where PVE sends the PSI pressure
	// metric as a string but cpu as a number.
	var s struct {
		Cpu      *client.PVEFloat `json:"cpu,omitempty"`
		Pressure *client.PVEFloat `json:"pressurecpusome,omitempty"`
	}

	body := `{"cpu":0.0123,"pressurecpusome":"0.42"}`

	err := json.Unmarshal([]byte(body), &s)
	if err != nil {
		t.Fatalf("Unmarshal struct: %v", err)
	}

	if s.Cpu == nil || s.Cpu.Float() != 0.0123 {
		t.Errorf("Cpu = %v, want 0.0123", s.Cpu)
	}

	if s.Pressure == nil || s.Pressure.Float() != 0.42 {
		t.Errorf("Pressure = %v, want 0.42", s.Pressure)
	}
}

func TestPVEFloat_Marshal(t *testing.T) {
	t.Parallel()

	out, err := json.Marshal(client.PVEFloat(1.5))
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}

	if string(out) != "1.5" {
		t.Errorf("Marshal(1.5) = %s, want 1.5", out)
	}

	// Non-finite values are not valid JSON; they marshal as 0.
	out, err = json.Marshal(client.PVEFloat(math.Inf(1)))
	if err != nil {
		t.Fatalf("Marshal(+Inf): %v", err)
	}

	if string(out) != "0" {
		t.Errorf("Marshal(+Inf) = %s, want 0", out)
	}
}

func TestPVEFloat_Invalid(t *testing.T) {
	t.Parallel()

	var f client.PVEFloat

	err := json.Unmarshal([]byte(`{}`), &f)
	if err == nil {
		t.Error("Unmarshal({}) expected an error, got nil")
	}
}
