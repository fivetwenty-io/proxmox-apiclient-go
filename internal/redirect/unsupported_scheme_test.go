package redirect_test

import (
	"errors"
	"net/url"
	"testing"

	"github.com/fivetwenty-io/proxmox-apiclient-go/v3/internal/permanent"
	"github.com/fivetwenty-io/proxmox-apiclient-go/v3/internal/redirect"
)

// TestScrubMarksAnUnsupportedSchemePermanent checks the error net/http builds
// for a followed redirect to a scheme it cannot speak. The failure repeats on
// every attempt, so it carries the permanent marker, and the message and the
// wrapped error stay as they were.
func TestScrubMarksAnUnsupportedSchemePermanent(t *testing.T) {
	t.Parallel()

	cause := &textError{`unsupported protocol scheme "ftp"`}
	original := &url.Error{Op: "Get", URL: "ftp://h/x?ticket=QSECRET", Err: cause}

	got := redirect.Scrub(original)
	if !permanent.Is(got) {
		t.Fatalf("%v is not permanent, want the marker", got)
	}

	if want := `Get "ftp://h/x": unsupported protocol scheme "ftp"`; got.Error() != want {
		t.Errorf("error = %q, want %q", got.Error(), want)
	}

	if !errors.Is(got, cause) {
		t.Error("Scrub lost the wrapped error")
	}

	again := redirect.Scrub(got)
	if again.Error() != got.Error() || !sameError(again, got) {
		t.Errorf("a second Scrub changed %v to %v, want the same error", got, again)
	}
}
