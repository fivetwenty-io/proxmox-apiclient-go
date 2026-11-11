package errors_test

import (
	"errors"
	"fmt"
	"strings"
	"testing"

	pvererrors "github.com/fivetwenty-io/proxmox-apiclient-go/v3/pkg/errors"
)

// TestTFATypesAreCappedInCountAndLength sends a 401 reply with half a million
// types and then one with a few types of 3 MB each, and checks that the error
// keeps 32 types, says how many it left out, and stays under the length cap.
func TestTFATypesAreCappedInCountAndLength(t *testing.T) {
	t.Parallel()

	const total = 500000

	body := `{"ticket":"PVE:t","challenge":"c","types":["` + strings.Repeat(`tot","`, total-1) + `tot"]}`

	var tfa *pvererrors.TFARequiredError
	if !errors.As(pvererrors.ParseAPIError(401, []byte(body)), &tfa) {
		t.Fatal("ParseAPIError did not return a TFARequiredError")
	}

	if len(tfa.Types) != pvererrors.MaxErrorItems {
		t.Errorf("kept %d types, want %d", len(tfa.Types), pvererrors.MaxErrorItems)
	}

	text := tfa.Error()
	if len(text) > pvererrors.MaxErrorTextBytes {
		t.Errorf("error is %d bytes, want at most %d", len(text), pvererrors.MaxErrorTextBytes)
	}

	want := fmt.Sprintf("and %d more", total-pvererrors.MaxErrorItems)
	if !strings.Contains(text, want) {
		t.Errorf("error %q does not say %q", text, want)
	}

	huge := strings.Repeat("x", 3<<20)
	body = `{"ticket":"PVE:t","types":["` + huge + `","` + huge + `","` + huge + `"]}`

	if !errors.As(pvererrors.ParseAPIError(401, []byte(body)), &tfa) {
		t.Fatal("ParseAPIError did not return a TFARequiredError")
	}

	if text = tfa.Error(); len(text) > pvererrors.MaxErrorTextBytes {
		t.Errorf("error with long types is %d bytes, want at most %d", len(text), pvererrors.MaxErrorTextBytes)
	}

	for _, kind := range tfa.Types {
		if len(kind) > pvererrors.MaxServerTextBytes+len("...(truncated)") {
			t.Errorf("a type of %d bytes was kept, want at most %d", len(kind), pvererrors.MaxServerTextBytes)
		}
	}

	short := pvererrors.ParseAPIError(401, []byte(`{"ticket":"PVE:t","types":["totp","yubico"]}`))
	if short.Error() != "two-factor authentication required (available types: totp, yubico)" {
		t.Errorf("a short list printed as %q, want it unchanged", short.Error())
	}
}

// TestErrorsMapIsCappedInCountAndLength sends a 400 reply with 200,000 field
// errors, and the same through the envelope constructor, and checks that the
// error keeps the 32 first names, says how many more there were, prints the
// same text every time, and stays under the length cap.
func TestErrorsMapIsCappedInCountAndLength(t *testing.T) {
	t.Parallel()

	const total = 200000

	var members strings.Builder

	fieldErrors := make(map[string]string, total)

	for i := range total {
		if i > 0 {
			members.WriteByte(',')
		}

		fmt.Fprintf(&members, `"k%06d":"v"`, i)

		fieldErrors[fmt.Sprintf("k%06d", i)] = "v"
	}

	body := `{"message":"bad","code":400,"errors":{` + members.String() + `}}`

	var param *pvererrors.ParameterError
	if !errors.As(pvererrors.ParseAPIError(400, []byte(body)), &param) {
		t.Fatal("ParseAPIError did not return a ParameterError")
	}

	checkCappedErrorsMap(t, "reply", &param.APIError, total)
	checkCappedErrorsMap(t, "envelope", pvererrors.NewEnvelopeError(200, "bad", fieldErrors), total)
}

// checkCappedErrorsMap checks one error built from total field errors named
// k000000 upward.
func checkCappedErrorsMap(t *testing.T, name string, apiErr *pvererrors.APIError, total int) {
	t.Helper()

	if len(apiErr.Errors) != pvererrors.MaxErrorItems {
		t.Errorf("%s: kept %d field errors, want %d", name, len(apiErr.Errors), pvererrors.MaxErrorItems)
	}

	for i := range pvererrors.MaxErrorItems {
		if _, kept := apiErr.Errors[fmt.Sprintf("k%06d", i)]; !kept {
			t.Errorf("%s: field k%06d was not kept, want the first names by sort order", name, i)
		}
	}

	text := apiErr.Error()
	if len(text) > pvererrors.MaxErrorTextBytes {
		t.Errorf("%s: error is %d bytes, want at most %d", name, len(text), pvererrors.MaxErrorTextBytes)
	}

	want := fmt.Sprintf("and %d more", total-pvererrors.MaxErrorItems)
	if !strings.HasSuffix(strings.TrimSuffix(text, ")"), want) {
		t.Errorf("%s: error %.200q does not end with %q", name, text, want)
	}

	for range 5 {
		if again := apiErr.Error(); again != text {
			t.Fatalf("%s: the error text changed between calls", name)
		}
	}
}

// TestErrorTextIsBoundedWhateverTheFieldSizes fills the message and 32 field
// errors with the longest text each may hold, and checks the error is cut at the
// total cap on a character boundary, with the marker. It also checks that an
// error built by hand with too many entries prints only the first 32, in name
// order.
func TestErrorTextIsBoundedWhateverTheFieldSizes(t *testing.T) {
	t.Parallel()

	long := strings.Repeat("\u00e9", 3000)
	fieldErrors := make(map[string]string, pvererrors.MaxErrorItems)

	for i := range pvererrors.MaxErrorItems {
		fieldErrors[fmt.Sprintf("%02d%s", i, long)] = long
	}

	text := pvererrors.NewEnvelopeError(200, long, fieldErrors).Error()
	if len(text) > pvererrors.MaxErrorTextBytes || !strings.HasSuffix(text, "...(truncated)") {
		t.Errorf("error is %d bytes ending %q, want at most %d ending in the marker",
			len(text), text[len(text)-20:], pvererrors.MaxErrorTextBytes)
	}

	if rawControl(text) {
		t.Error("the cut error text is not valid UTF-8 or holds a raw control")
	}

	byHand := &pvererrors.APIError{Message: "m", Code: 400, Errors: map[string]string{}}
	for i := range 1000 {
		byHand.Errors[fmt.Sprintf("f%04d", i)] = "v"
	}

	got := byHand.Error()
	if !strings.Contains(got, "f0031: v, and 968 more)") || strings.Contains(got, "f0032") {
		t.Errorf("hand-built error printed %q, want the first 32 names and a count of 968 more", got)
	}
}
