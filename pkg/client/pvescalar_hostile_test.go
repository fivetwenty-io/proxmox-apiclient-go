package client_test

import (
	"encoding/json"
	"strings"
	"testing"
	"unicode"
	"unicode/utf8"

	"github.com/fivetwenty-io/proxmox-apiclient-go/v3/pkg/client"
)

// scalarUnmarshalers returns a function for each PVE scalar that decodes data
// the way the type's UnmarshalJSON does.
func scalarUnmarshalers() map[string]func(data []byte) error {
	return map[string]func(data []byte) error{
		"bool": func(data []byte) error {
			var v client.PVEBool

			return v.UnmarshalJSON(data)
		},
		"int": func(data []byte) error {
			var v client.PVEInt

			return v.UnmarshalJSON(data)
		},
		"float": func(data []byte) error {
			var v client.PVEFloat

			return v.UnmarshalJSON(data)
		},
	}
}

// hasRawControl reports whether text holds a control character, a text
// direction override, or invalid UTF-8.
func hasRawControl(text string) bool {
	if !utf8.ValidString(text) {
		return true
	}

	for _, r := range text {
		if unicode.IsControl(r) || unicode.Is(unicode.Cf, r) {
			return true
		}
	}

	return false
}

// TestPVEScalarErrorsEscapeTheServersBytes hands each unmarshaler a value it
// cannot read, with control characters in it, and checks that the error shows
// the value escaped.
func TestPVEScalarErrorsEscapeTheServersBytes(t *testing.T) {
	t.Parallel()

	hostile := map[string]string{
		"array with newline": "[\n\"forged log line\"\n]",
		"object with escape": "{\"a\":\x1b[2J}",
		"direction override": "[\"\u202eevil\"]",
		"invalid utf-8":      "[\"\xff\xfe\"]",
		"nul":                "[\x00]",
	}

	for kind, unmarshal := range scalarUnmarshalers() {
		for name, data := range hostile {
			t.Run(kind+"/"+name, func(t *testing.T) {
				t.Parallel()

				err := unmarshal([]byte(data))
				if err == nil {
					t.Fatalf("%q decoded, want an error", data)
				}

				if hasRawControl(err.Error()) {
					t.Errorf("error %q holds a raw control character", err.Error())
				}

				if !strings.Contains(err.Error(), "unsupported JSON value") {
					t.Errorf("error %q lost its kind", err.Error())
				}
			})
		}
	}
}

// TestPVEScalarErrorsCapTheQuotedValue checks that a long value from the
// server does not fill the message, and that its start is still shown.
func TestPVEScalarErrorsCapTheQuotedValue(t *testing.T) {
	t.Parallel()

	data := "[\"" + strings.Repeat("x", 5000) + "\"]"

	for kind, unmarshal := range scalarUnmarshalers() {
		t.Run(kind, func(t *testing.T) {
			t.Parallel()

			err := unmarshal([]byte(data))
			if err == nil {
				t.Fatal("decoded, want an error")
			}

			if len(err.Error()) > 300 {
				t.Errorf("error is %d bytes, want it capped", len(err.Error()))
			}

			if !strings.Contains(err.Error(), `"[\"xxxx`) || !strings.Contains(err.Error(), `"...`) {
				t.Errorf("error %q should show the start of the value and an ellipsis", err.Error())
			}
		})
	}
}

// TestPVEScalarsStillDecodeNormalValues checks that the quoting touched only
// the error path.
func TestPVEScalarsStillDecodeNormalValues(t *testing.T) {
	t.Parallel()

	var fields struct {
		Enabled client.PVEBool  `json:"enabled"`
		Count   client.PVEInt   `json:"count"`
		Ratio   client.PVEFloat `json:"ratio"`
	}

	err := json.Unmarshal([]byte(`{"enabled":"1","count":"7","ratio":0.5}`), &fields)
	if err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}

	if !bool(fields.Enabled) || int64(fields.Count) != 7 || float64(fields.Ratio) != 0.5 {
		t.Errorf("fields = %+v", fields)
	}
}
