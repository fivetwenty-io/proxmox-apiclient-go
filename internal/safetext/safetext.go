// Package safetext keeps bytes a server chose from reaching an error message
// or a log line as raw control characters. A hostile or broken server can put
// a newline, an escape sequence, or a text direction override in a value the
// client then prints, which forges log lines or rewrites what a terminal shows.
package safetext

import (
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"
)

// MaxQuoted is the most bytes of a server's value that Quote keeps.
const MaxQuoted = 64

// ellipsis follows a quoted value that Quote cut short.
const ellipsis = "..."

// Quote renders value as a Go string literal, with every control character,
// text direction override, and invalid byte escaped. It keeps at most
// MaxQuoted bytes of value, cut on a character boundary, and follows the
// closing quote with an ellipsis when it cut anything, so the message stays
// short and still shows what the server sent.
func Quote(value string) string {
	cut := len(value)

	if cut > MaxQuoted {
		cut = MaxQuoted
		// Back up to the start of a character, so the cut does not split one.
		for cut > 0 && !utf8.RuneStart(value[cut]) {
			cut--
		}
	}

	quoted := strconv.Quote(value[:cut])
	if cut < len(value) {
		quoted += ellipsis
	}

	return quoted
}

// Error returns err, or when its text holds a control character, a text
// direction override, or invalid UTF-8, an error with the same chain whose
// text has those escaped. An error with clean text comes back as the same
// value.
func Error(err error) error {
	if err == nil {
		return nil
	}

	text := err.Error()
	if !needsEscape(text) {
		return err
	}

	return &escapedError{text: escape(text), cause: err}
}

// escapedError is an error whose text is the escaped text of the error it
// wraps. errors.Is and errors.As see the wrapped error.
type escapedError struct {
	text  string
	cause error
}

// Error returns the escaped text.
func (e *escapedError) Error() string { return e.text }

// Unwrap returns the error whose text was escaped.
func (e *escapedError) Unwrap() error { return e.cause }

// GoString keeps the %#v form to the escaped text, because the default form
// would print the wrapped error and with it the raw bytes.
func (e *escapedError) GoString() string { return "safetext.Error(" + strconv.Quote(e.text) + ")" }

// unsafeRune reports whether r must not reach a log line as itself.
func unsafeRune(r rune) bool {
	return unicode.IsControl(r) || unicode.Is(unicode.Cf, r) || r == '\u2028' || r == '\u2029'
}

// needsEscape reports whether text holds a rune that escape would change.
func needsEscape(text string) bool {
	for i := 0; i < len(text); {
		r, size := utf8.DecodeRuneInString(text[i:])
		if (r == utf8.RuneError && size == 1) || unsafeRune(r) {
			return true
		}

		i += size
	}

	return false
}

// escape replaces each unsafe rune and each invalid byte in text with the
// escape sequence strconv.Quote would use for it.
func escape(text string) string {
	var out strings.Builder

	for i := 0; i < len(text); {
		r, size := utf8.DecodeRuneInString(text[i:])

		if (r == utf8.RuneError && size == 1) || unsafeRune(r) {
			out.WriteString(escapeText(text[i : i+size]))
		} else {
			out.WriteString(text[i : i+size])
		}

		i += size
	}

	return out.String()
}

// escapeText quotes one character and removes the quote marks around it.
func escapeText(char string) string {
	quoted := strconv.Quote(char)

	return quoted[1 : len(quoted)-1]
}
