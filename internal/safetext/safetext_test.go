package safetext_test

import (
	"errors"
	"fmt"
	"strings"
	"testing"
	"unicode"
	"unicode/utf8"

	"github.com/fivetwenty-io/proxmox-apiclient-go/v3/internal/safetext"
)

// hasRawControl reports whether text holds a control character, a text
// direction override, a line separator, or invalid UTF-8.
func hasRawControl(text string) bool {
	if !utf8.ValidString(text) {
		return true
	}

	for _, r := range text {
		if unicode.IsControl(r) || unicode.Is(unicode.Cf, r) || r == '\u2028' || r == '\u2029' {
			return true
		}
	}

	return false
}

// quotedAs is the quoted form of count letters a followed by suffix.
func quotedAs(count int, suffix string) string {
	return `"` + strings.Repeat("a", count) + `"` + suffix
}

func TestQuoteEscapesControlCharactersAndCutsLongValues(t *testing.T) {
	t.Parallel()

	tests := map[string]struct{ in, want string }{
		"plain":           {"abc", `"abc"`},
		"empty":           {"", `""`},
		"newline":         {"a\nb", `"a\nb"`},
		"escape sequence": {"\x1b[2J", `"\x1b[2J"`},
		"nul":             {"\x00", `"\x00"`},
		"delete":          {"\x7f", `"\x7f"`},
		"carriage return": {"ok\r\nforged line", `"ok\r\nforged line"`},
		"direction":       {"a\u202eb", `"a\u202eb"`},
		"line separator":  {"a\u2028b", `"a\u2028b"`},
		"invalid utf-8":   {"a\xffb", `"a\xffb"`},
		"kept unicode":    {"héllo", `"héllo"`},
		"exactly the cap": {strings.Repeat("a", safetext.MaxQuoted), quotedAs(safetext.MaxQuoted, "")},
		"one over":        {strings.Repeat("a", safetext.MaxQuoted+1), quotedAs(safetext.MaxQuoted, "...")},
		"cut mid rune": {
			strings.Repeat("a", safetext.MaxQuoted-1) + "é" + "tail",
			quotedAs(safetext.MaxQuoted-1, "..."),
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			got := safetext.Quote(tc.in)
			if got != tc.want {
				t.Errorf("Quote(%q) = %s, want %s", tc.in, got, tc.want)
			}

			if hasRawControl(got) {
				t.Errorf("Quote(%q) = %q holds a raw control character", tc.in, got)
			}
		})
	}
}

func TestQuoteKeepsTheQuotedLengthBounded(t *testing.T) {
	t.Parallel()

	got := safetext.Quote(strings.Repeat("\x00", 100000))

	// 64 kept bytes, each escaped to four characters, plus quotes and the ellipsis.
	if limit := safetext.MaxQuoted*4 + len(`""...`); len(got) != limit {
		t.Errorf("len = %d, want %d", len(got), limit)
	}
}

var errCause = errors.New("sentinel")

func TestErrorEscapesTextAndKeepsTheChain(t *testing.T) {
	t.Parallel()

	dirty := fmt.Errorf("decode: line one\nline two \x1b[31m\u202e \xff: %w", errCause)

	got := safetext.Error(dirty)

	if hasRawControl(got.Error()) {
		t.Errorf("text %q holds a raw control character", got.Error())
	}

	want := `decode: line one\nline two \x1b[31m\u202e \xff: sentinel`
	if got.Error() != want {
		t.Errorf("text = %q, want %q", got.Error(), want)
	}

	if !errors.Is(got, errCause) || !errors.Is(got, dirty) {
		t.Errorf("the chain of %v lost the original", got)
	}

	if hasRawControl(fmt.Sprintf("%#v", got)) || hasRawControl(fmt.Sprintf("%+v", got)) {
		t.Error("the verbose forms hold a raw control character")
	}
}

func TestErrorReturnsCleanTextAsTheSameValue(t *testing.T) {
	t.Parallel()

	clean := fmt.Errorf("pve bool: héllo \uFFFD: %w", errCause)

	if got := safetext.Error(clean); got != clean { //nolint:errorlint // the same value is the contract
		t.Errorf("Error returned %v, want the same error", got)
	}

	if safetext.Error(nil) != nil {
		t.Error("Error(nil) must be nil")
	}
}
