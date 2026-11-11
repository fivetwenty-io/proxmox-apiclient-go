package errors_test

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"unicode"
	"unicode/utf8"

	pvererrors "github.com/fivetwenty-io/proxmox-apiclient-go/v3/pkg/errors"
)

const (
	jsonKeyMessage = "message"
	shortField     = "short"
)

// rawControl reports whether text holds a control character other than newline
// and tab, a text direction or other format character, a line separator, or
// invalid UTF-8.
func rawControl(text string) bool {
	if !utf8.ValidString(text) {
		return true
	}

	for _, r := range text {
		if r == '\n' || r == '\t' {
			continue
		}

		if unicode.IsControl(r) || unicode.Is(unicode.Cf, r) || r == '\u2028' || r == '\u2029' {
			return true
		}
	}

	return false
}

// TestParseAPIErrorEscapesServerBytes sends hostile text in every field the
// server chooses, for every error type, and checks the printed error holds no
// raw control character while a newline survives.
func TestParseAPIErrorEscapesServerBytes(t *testing.T) {
	t.Parallel()

	const hostile = "\x1b[2J\x1b]0;title\a\r\u202eFLIP\u2066\u200e\u061c\x00\x7f"

	message, marshalErr := json.Marshal(map[string]interface{}{jsonKeyMessage: "first line\n" + hostile, "code": 500})
	if marshalErr != nil {
		t.Fatalf("marshal: %v", marshalErr)
	}

	bodies := map[string]string{
		"message": string(message),
		"errors":  `{"message":"bad","errors":{"vm\u001bid":"\u001b]0;x\u0007\u202e"},"code":400}`,
		"file":    `{"message":"bad","file":"\u001b[31m/x.pm\r","code":500}`,
		"tfa":     `{"ticket":"PVE:t","challenge":"c\u202e","types":["tot\u001bp","\u202eyubico"]}`,
		"empty":   `{"message":"\u001b","code":401}`,
		"text":    "<html>" + hostile + "</html>",
	}

	for _, status := range []int{400, 401, 403, 404, 500} {
		for name, body := range bodies {
			t.Run(name, func(t *testing.T) {
				t.Parallel()

				err := pvererrors.ParseAPIError(status, []byte(body))
				if err == nil {
					t.Fatal("ParseAPIError returned nil")
				}

				if rawControl(err.Error()) {
					t.Errorf("status %d: error text %q holds a raw control", status, err.Error())
				}
			})
		}
	}

	err := pvererrors.ParseAPIError(500, []byte(`{"message":"line one\nline two","code":500}`))
	if !strings.Contains(err.Error(), "line one\nline two") {
		t.Errorf("error text = %q, want the newline kept", err.Error())
	}
}

// TestParseAPIErrorKeepsTheEscapedFieldsForPrograms checks the fields a program
// reads hold the escaped text, not the raw bytes.
func TestParseAPIErrorKeepsTheEscapedFieldsForPrograms(t *testing.T) {
	t.Parallel()

	err := pvererrors.ParseAPIError(400, []byte(`{"message":"a\u001bb","errors":{"vmid":"x\u202ey"},"code":400}`))

	var param *pvererrors.ParameterError
	if !errors.As(err, &param) {
		t.Fatalf("error %T is not a ParameterError", err)
	}

	if param.Message != `a\x1bb` || param.Errors["vmid"] != `x\u202ey` {
		t.Errorf("fields = %q and %q, want escaped text", param.Message, param.Errors["vmid"])
	}
}

// TestParseAPIErrorCapsABodyThatIsNotJSON checks a large page is cut at 1 KiB
// with a marker, and that a small one is kept whole.
func TestParseAPIErrorCapsABodyThatIsNotJSON(t *testing.T) {
	t.Parallel()

	page := "<html><title>Bad Gateway</title>" + strings.Repeat("\u001b[2J é ", 100000) + "</html>"

	err := pvererrors.ParseAPIError(502, []byte(page))

	var apiErr *pvererrors.APIError
	if !errors.As(err, &apiErr) {
		t.Fatalf("error %T is not an APIError", err)
	}

	if len(apiErr.Message) > pvererrors.MaxGenericMessageBytes+len("...(truncated)") {
		t.Errorf("message is %d bytes, want at most %d plus the marker",
			len(apiErr.Message), pvererrors.MaxGenericMessageBytes)
	}

	if !strings.HasSuffix(apiErr.Message, "...(truncated)") || !strings.Contains(apiErr.Message, "Bad Gateway") {
		t.Errorf("message = %.80q..., want the start of the page and a marker", apiErr.Message)
	}

	if rawControl(err.Error()) {
		t.Error("the cut page text holds a raw control")
	}

	small := pvererrors.ParseAPIError(502, []byte("  upstream down \n"))
	if !strings.Contains(small.Error(), "upstream down (code: 502)") {
		t.Errorf("small body error = %q, want it kept whole", small.Error())
	}
}

// TestServerTextIsCappedWithAMarker sends a message, a field name, a field
// message, and a file name far longer than the cap, as a failure reply and as
// an envelope on a success status, and checks every text field is cut with the
// marker, on a character boundary, while a short field is left alone.
func TestServerTextIsCappedWithAMarker(t *testing.T) {
	t.Parallel()

	const marker = "...(truncated)"

	long := strings.Repeat("\u00e9", 3000)
	limit := pvererrors.MaxServerTextBytes + len(marker)

	body, err := json.Marshal(map[string]interface{}{
		jsonKeyMessage: long, "file": long, "errors": map[string]string{long: long, shortField: "ok"},
	})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	var parsed *pvererrors.APIError

	if !errors.As(pvererrors.ParseAPIError(500, body), &parsed) {
		t.Fatal("ParseAPIError did not return an *APIError")
	}

	envelope := pvererrors.NewEnvelopeError(200, long, map[string]string{long: long, shortField: "ok"})
	envelope.File = ""

	for name, apiErr := range map[string]*pvererrors.APIError{"failure": parsed, "envelope": envelope} {
		fields := []string{apiErr.Message}
		if name == "failure" {
			fields = append(fields, apiErr.File)
		}

		for field, msg := range apiErr.Errors {
			fields = append(fields, field, msg)
		}

		for _, text := range fields {
			if text == shortField || text == "ok" {
				continue
			}

			if len(text) > limit || !strings.HasSuffix(text, marker) || !utf8.ValidString(text) {
				t.Errorf("%s: field of %d bytes, want a valid cut of at most %d bytes ending in %q",
					name, len(text), limit, marker)
			}
		}

		if apiErr.Errors[shortField] != "ok" {
			t.Errorf("%s: the short field changed: %v", name, apiErr.Errors)
		}
	}
}

// TestNewEnvelopeErrorEscapesAndKeepsTheStatus checks the constructor keeps the
// status code and the sentinel the status class gives, and escapes the text.
func TestNewEnvelopeErrorEscapesAndKeepsTheStatus(t *testing.T) {
	t.Parallel()

	apiErr := pvererrors.NewEnvelopeError(200, "a\u001b[2J\u202eb\nc", map[string]string{"k\r": "v\u0007"})

	if apiErr.Code != 200 || apiErr.HTTPCode != 200 {
		t.Errorf("code = %d, http code = %d, want 200", apiErr.Code, apiErr.HTTPCode)
	}

	if rawControl(apiErr.Error()) || !strings.Contains(apiErr.Message, "\n") {
		t.Errorf("error text %q: want no raw character and the newline kept", apiErr.Error())
	}

	if pvererrors.NewEnvelopeError(500, "m", nil).Unwrap() == nil {
		t.Error("a 500 envelope lost its server error sentinel")
	}
}
