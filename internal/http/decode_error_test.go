package http //nolint:testpackage // white-box test: reaches the response parser and the client's response path

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
	"unicode"
	"unicode/utf8"
)

// errHostileTarget is the cause a hostile target type reports, with control
// characters in its text as a decoder that echoes server bytes would produce.
var errHostileTarget = errors.New("bad value\nforged: log line \x1b[31m\u202e")

// hostileTarget is a decode target whose UnmarshalJSON fails with text that
// holds control characters.
type hostileTarget struct{}

func (*hostileTarget) UnmarshalJSON([]byte) error { return errHostileTarget }

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

func jsonResponse(t *testing.T, body string) *http.Response {
	t.Helper()

	req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, "http://example.com/path", nil)
	if err != nil {
		t.Fatalf("NewRequest: %v", err)
	}

	return &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{testHeaderContentType: []string{testContentTypeJSON}},
		Body:       io.NopCloser(bytes.NewReader([]byte(body))),
		Request:    req,
	}
}

// TestParseJSONErrorsEscapeControlCharacters decodes into a target whose
// decoder fails with control characters in its message, through each place the
// parser reports a decode failure, and checks that the error text holds none
// of them while the cause stays reachable.
func TestParseJSONErrorsEscapeControlCharacters(t *testing.T) {
	t.Parallel()

	tests := map[string]struct {
		body   string
		strict bool
	}{
		"envelope data":      {`{"data":{"x":1}}`, false},
		"whole body":         {`{"x":1}`, false},
		"not an envelope":    {`[1,2]`, false},
		"strict not envelop": {`[1,2]`, true},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			parser := NewResponseParser()
			parser.StrictMode = tc.strict

			err := parser.Parse(jsonResponse(t, tc.body), &hostileTarget{})
			if err == nil {
				t.Fatal("Parse succeeded, want a decode error")
			}

			if hasRawControl(err.Error()) {
				t.Errorf("error %q holds a raw control character", err.Error())
			}

			if !tc.strict && !errors.Is(err, errHostileTarget) {
				t.Errorf("error %v lost its cause", err)
			}
		})
	}
}

// TestDecodeErrorsFromHostileBodiesHoldNoControlCharacters sends bodies with
// control bytes in invalid JSON through the parser and the client, where the
// standard library's own error text must come back escaped.
func TestDecodeErrorsFromHostileBodiesHoldNoControlCharacters(t *testing.T) {
	t.Parallel()

	bodies := []string{
		"{\"data\":\x1b[2J}",
		"{\"data\":\"a\nb\"}",
		"{\"data\":\x00}",
		"\u202e{\"data\":1}",
		"{\"data\":{\"a\":\xff}}",
	}

	for _, body := range bodies {
		strictParser := NewResponseParser()
		strictParser.StrictMode = true

		for _, parser := range []*ResponseParser{NewResponseParser(), strictParser} {
			var target map[string]interface{}

			err := parser.Parse(jsonResponse(t, body), &target)
			if err != nil && hasRawControl(err.Error()) {
				t.Errorf("parser error %q for body %q holds a raw control character", err.Error(), body)
			}
		}

		server := newTestServer(t, func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set(testHeaderContentType, testContentTypeJSON)
			_, _ = w.Write([]byte(body))
		})
		client := newClientForServerURL(t, server.URL, func(o *Options) { o.APIToken = testAPITokenFull })

		_, err := client.DoWithContext(context.Background(), http.MethodGet, "/x", nil)
		if err != nil && (hasRawControl(err.Error()) || strings.ContainsAny(err.Error(), "\r\n")) {
			t.Errorf("client error %q for body %q holds a raw control character", err.Error(), body)
		}
	}
}
