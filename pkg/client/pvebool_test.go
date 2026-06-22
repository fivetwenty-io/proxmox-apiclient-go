package client_test

import (
	"encoding/json"
	"testing"

	"github.com/fivetwenty-io/pve-apiclient-go/v3/pkg/client"
)

func TestPVEBool_Unmarshal(t *testing.T) {
	t.Parallel()

	cases := []struct {
		in   string
		want bool
	}{
		{`true`, true},
		{`false`, false},
		{`1`, true},
		{`0`, false},
		{`2`, true},
		{`"1"`, true},
		{`"0"`, false},
		{`"true"`, true},
		{`"false"`, false},
		{`"yes"`, true},
		{`"no"`, false},
		{`"on"`, true},
		{`"off"`, false},
		{`""`, false},
		{`"TRUE"`, true},
		{` 1 `, true},
	}

	for _, tc := range cases {
		var b client.PVEBool

		err := json.Unmarshal([]byte(tc.in), &b)
		if err != nil {
			t.Fatalf("Unmarshal(%s): unexpected error: %v", tc.in, err)
		}

		if b.Bool() != tc.want {
			t.Errorf("Unmarshal(%s) = %v, want %v", tc.in, b.Bool(), tc.want)
		}
	}
}

func TestPVEBool_UnmarshalNullLeavesUnchanged(t *testing.T) {
	t.Parallel()

	b := client.PVEBool(true)

	err := json.Unmarshal([]byte("null"), &b)
	if err != nil {
		t.Fatalf("Unmarshal(null): %v", err)
	}

	if !b.Bool() {
		t.Errorf("Unmarshal(null) mutated value to false")
	}
}

func TestPVEBool_UnmarshalInStruct(t *testing.T) {
	t.Parallel()

	type resp struct {
		Enable *client.PVEBool `json:"enable,omitempty"`
		Agent  *client.PVEBool `json:"agent,omitempty"`
	}

	var r resp
	// enable as number 1, agent absent.
	err := json.Unmarshal([]byte(`{"enable":1}`), &r)
	if err != nil {
		t.Fatalf("Unmarshal struct: %v", err)
	}

	if r.Enable == nil || !r.Enable.Bool() {
		t.Errorf("Enable = %v, want true", r.Enable)
	}

	if r.Agent != nil {
		t.Errorf("Agent = %v, want nil (absent)", r.Agent)
	}
}

func TestPVEBool_Marshal(t *testing.T) {
	t.Parallel()

	got, err := json.Marshal(client.PVEBool(true))
	if err != nil {
		t.Fatalf("Marshal(true): %v", err)
	}

	if string(got) != "true" {
		t.Errorf("Marshal(true) = %s, want true", got)
	}

	got, err = json.Marshal(client.PVEBool(false))
	if err != nil {
		t.Fatalf("Marshal(false): %v", err)
	}

	if string(got) != "false" {
		t.Errorf("Marshal(false) = %s, want false", got)
	}
}

func TestPVEBool_Invalid(t *testing.T) {
	t.Parallel()

	var b client.PVEBool

	err := json.Unmarshal([]byte(`{"x":1}`), &b)
	if err == nil {
		t.Errorf("Unmarshal(object) expected error, got nil")
	}
}
