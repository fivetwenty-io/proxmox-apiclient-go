package client_test

import (
	"encoding/json"
	"testing"

	"github.com/fivetwenty-io/proxmox-apiclient-go/v3/pkg/client"
)

func TestPVEInt_Unmarshal(t *testing.T) {
	t.Parallel()

	cases := []struct {
		in   string
		want int64
	}{
		{`5`, 5},
		{`0`, 0},
		{`-3`, -3},
		{`9223372036854775807`, 9223372036854775807},
		{`1.9`, 1},  // fractional numbers truncate toward zero
		{`-1.9`, -1},
		{`"5"`, 5},
		{`"0"`, 0},
		{`"-3"`, -3},
		{`""`, 0}, // PVE renders an absent integer as an empty string
		{`"  7 "`, 7},
		{`"2.9"`, 2},     // fractional strings truncate toward zero
		{`"garbage"`, 0}, // unparseable strings yield 0, never an error
	}

	for _, tc := range cases {
		var i client.PVEInt

		err := json.Unmarshal([]byte(tc.in), &i)
		if err != nil {
			t.Fatalf("Unmarshal(%s): unexpected error: %v", tc.in, err)
		}

		if i.Int() != tc.want {
			t.Errorf("Unmarshal(%s) = %v, want %v", tc.in, i.Int(), tc.want)
		}
	}
}

func TestPVEInt_UnmarshalNullLeavesValue(t *testing.T) {
	t.Parallel()

	i := client.PVEInt(9)

	err := json.Unmarshal([]byte(`null`), &i)
	if err != nil {
		t.Fatalf("Unmarshal(null): unexpected error: %v", err)
	}

	if i.Int() != 9 {
		t.Errorf("Unmarshal(null) changed value to %v, want 9", i.Int())
	}
}

func TestPVEInt_InStruct(t *testing.T) {
	t.Parallel()

	// A struct mirroring a firewall rule payload where PVE sends pos as a
	// string but enable as a number.
	var s struct {
		Pos    client.PVEInt  `json:"pos"`
		Enable *client.PVEInt `json:"enable,omitempty"`
	}

	body := `{"pos":"2","enable":1}`

	err := json.Unmarshal([]byte(body), &s)
	if err != nil {
		t.Fatalf("Unmarshal struct: %v", err)
	}

	if s.Pos.Int() != 2 {
		t.Errorf("Pos = %v, want 2", s.Pos.Int())
	}

	if s.Enable == nil || s.Enable.Int() != 1 {
		t.Errorf("Enable = %v, want 1", s.Enable)
	}
}

func TestPVEInt_Marshal(t *testing.T) {
	t.Parallel()

	out, err := json.Marshal(client.PVEInt(-42))
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}

	if string(out) != "-42" {
		t.Errorf("Marshal(-42) = %s, want -42", out)
	}
}

func TestPVEInt_Invalid(t *testing.T) {
	t.Parallel()

	var i client.PVEInt

	err := json.Unmarshal([]byte(`{}`), &i)
	if err == nil {
		t.Error("Unmarshal({}) expected an error, got nil")
	}
}
