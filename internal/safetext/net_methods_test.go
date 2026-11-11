package safetext_test

import (
	"errors"
	"net"
	"testing"

	"github.com/fivetwenty-io/proxmox-apiclient-go/v3/internal/safetext"
)

// errTwoLines is a cause whose text holds a newline.
var errTwoLines = errors.New("a\nb")

// lineTimeoutError is a timeout whose text holds a newline, so escaping wraps it.
type lineTimeoutError struct{}

func (lineTimeoutError) Error() string   { return "read timed out\nsecond line" }
func (lineTimeoutError) Timeout() bool   { return true }
func (lineTimeoutError) Temporary() bool { return false }

// plainWrapperError wraps an error and has no Timeout or Temporary method.
type plainWrapperError struct{ inner error }

func (e *plainWrapperError) Error() string { return "body read: " + e.inner.Error() }
func (e *plainWrapperError) Unwrap() error { return e.inner }

// timeoutOnlyError has a Timeout method and no Temporary method.
type timeoutOnlyError struct{}

func (timeoutOnlyError) Error() string { return "deadline\npassed" }
func (timeoutOnlyError) Timeout() bool { return true }

// temporaryOnlyError has a Temporary method and no Timeout method.
type temporaryOnlyError struct{}

func (temporaryOnlyError) Error() string   { return "busy\ntry again" }
func (temporaryOnlyError) Temporary() bool { return true }

// TestEscapedWrapperDoesNotHideADeeperTimeout escapes a wrapper that has no
// Timeout method around a timeout. errors.As for a net.Error found the timeout
// before the text was escaped, and it must still find it.
func TestEscapedWrapperDoesNotHideADeeperTimeout(t *testing.T) {
	t.Parallel()

	raw := &plainWrapperError{inner: lineTimeoutError{}}

	var before net.Error
	if !errors.As(raw, &before) || !before.Timeout() {
		t.Fatal("the raw error does not read as a timeout")
	}

	for name, escaped := range map[string]error{
		"Error":       safetext.Error(raw),
		"CappedError": safetext.CappedError(raw, safetext.MaxErrorTextBytes),
	} {
		var after net.Error
		if !errors.As(escaped, &after) || !after.Timeout() {
			t.Errorf("%s: %v no longer reads as a timeout through errors.As", name, escaped)
		}
	}
}

// TestEscapedWrapperHasTheMethodsOfItsCause checks that the escaped error has
// a Timeout or a Temporary method exactly when its cause has one, and answers
// as the cause does.
func TestEscapedWrapperHasTheMethodsOfItsCause(t *testing.T) {
	t.Parallel()

	cases := map[string]struct {
		cause                    error
		timeout, temporary       bool
		hasTimeout, hasTemporary bool
	}{
		"net error":      {lineTimeoutError{}, true, false, true, true},
		"timeout only":   {timeoutOnlyError{}, true, false, true, false},
		"temporary only": {temporaryOnlyError{}, false, true, false, true},
		"neither":        {&plainWrapperError{inner: errTwoLines}, false, false, false, false},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			got := safetext.Error(tc.cause)
			if got.Error() == tc.cause.Error() {
				t.Fatalf("%q was not escaped, so the case tests nothing", got.Error())
			}

			timeout, hasTimeout := got.(interface{ Timeout() bool })
			if hasTimeout != tc.hasTimeout || (hasTimeout && timeout.Timeout() != tc.timeout) {
				t.Errorf("Timeout method present %v, want %v, answering %v", hasTimeout, tc.hasTimeout, tc.timeout)
			}

			temporary, hasTemporary := got.(interface{ Temporary() bool })
			if hasTemporary != tc.hasTemporary || (hasTemporary && temporary.Temporary() != tc.temporary) {
				t.Errorf("Temporary method present %v, want %v, answering %v", hasTemporary, tc.hasTemporary,
					tc.temporary)
			}

			if !errors.Is(got, tc.cause) {
				t.Errorf("%v no longer unwraps to its cause", got)
			}
		})
	}
}
