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

	got := safetext.Error(clean)
	if !errors.Is(got, clean) {
		t.Errorf("Error returned %v, want the same error", got)
	}

	if safetext.Error(nil) != nil {
		t.Error("Error(nil) must be nil")
	}
}

// TestMultilineEscapesEveryControlButNewlineAndTab checks each control
// character, the direction controls, and invalid UTF-8, and that the layout
// characters survive.
func TestMultilineEscapesEveryControlButNewlineAndTab(t *testing.T) {
	t.Parallel()

	hostile := map[string]string{
		"escape":             "a\x1b[2Jb",
		"carriage return":    "a\rb",
		"nul":                "a\x00b",
		"delete":             "a\x7fb",
		"c1 control":         "a\u009bb",
		"left-to-right emb":  "a\u202ab",
		"right-to-left ovr":  "a\u202eb",
		"pop directional":    "a\u202cb",
		"isolate":            "a\u2066b",
		"pop isolate":        "a\u2069b",
		"left-to-right mark": "a\u200eb",
		"right-to-left mark": "a\u200fb",
		"arabic letter mark": "a\u061cb",
		"line separator":     "a\u2028b",
		"invalid utf8":       "a\xffb",
	}

	for name, text := range hostile {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			got := safetext.Multiline(text)
			if hasRawControl(got) {
				t.Errorf("Multiline(%q) = %q still holds a raw control", text, got)
			}

			if !strings.HasPrefix(got, "a") || !strings.HasSuffix(got, "b") {
				t.Errorf("Multiline(%q) = %q lost the text around the control", text, got)
			}
		})
	}

	const layout = "line one\n\tline two\n"
	if got := safetext.Multiline(layout); got != layout {
		t.Errorf("Multiline kept %q as %q, want newline and tab unchanged", layout, got)
	}

	if got := safetext.Line(layout); strings.ContainsAny(got, "\n\t") {
		t.Errorf("Line(%q) = %q, want newline and tab escaped", layout, got)
	}
}

// TestCappedStopsAtTheLimitOnACharacterBoundary checks the bound is on the
// escaped output and that the marker shows a cut.
func TestCappedStopsAtTheLimitOnACharacterBoundary(t *testing.T) {
	t.Parallel()

	got := safetext.Capped(strings.Repeat("\u202e", 100), 20)
	if !strings.HasSuffix(got, "...(truncated)") {
		t.Errorf("Capped = %q, want the truncation marker", got)
	}

	if body := strings.TrimSuffix(got, "...(truncated)"); len(body) > 20 || hasRawControl(body) {
		t.Errorf("Capped body = %q, want at most 20 escaped bytes", body)
	}

	if short := safetext.Capped("fits", 20); short != "fits" {
		t.Errorf("Capped(fits) = %q, want it unchanged", short)
	}

	multi := safetext.CappedMultilineBytes([]byte(strings.Repeat("é", 4000)), 10)
	if !strings.HasSuffix(multi, "...(truncated)") || !utf8.ValidString(multi) {
		t.Errorf("CappedMultilineBytes = %q, want valid text with the marker", multi)
	}
}

var (
	errCappedSentinel = errors.New("sentinel")
	errCappedClean    = errors.New("clean")
)

// TestCappedErrorKeepsTheChainAndBoundsTheText checks the error form.
func TestCappedErrorKeepsTheChainAndBoundsTheText(t *testing.T) {
	t.Parallel()

	sentinel := errCappedSentinel
	err := fmt.Errorf("%w: \x1b[2J%s", sentinel, strings.Repeat("x", 500))

	got := safetext.CappedError(err, 64)
	if !errors.Is(got, sentinel) {
		t.Error("CappedError lost the chain")
	}

	if hasRawControl(got.Error()) || len(got.Error()) > 64+len("...(truncated)") {
		t.Errorf("CappedError text = %q, want escaped and bounded", got.Error())
	}

	clean := errCappedClean
	if !errors.Is(safetext.CappedError(clean, 64), clean) {
		t.Error("an error that fits and is clean must come back unchanged")
	}

	if safetext.CappedError(nil, 64) != nil {
		t.Error("nil must stay nil")
	}
}
