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

	message, marshalErr := json.Marshal(map[string]interface{}{"message": "first line\n" + hostile, "code": 500})
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
