package auth

// tfa_internal_test.go tests InteractiveTFAHandler and related unexported methods.
// Only internal (package auth) tests can access unexported fields like reader.

import (
	"bufio"
	"errors"
	"strings"
	"testing"
)

// newInteractiveTFAHandlerWithReader creates an InteractiveTFAHandler that
// reads from r instead of os.Stdin. Used only in tests.
func newInteractiveTFAHandlerWithReader(r *bufio.Reader) *InteractiveTFAHandler {
	return &InteractiveTFAHandler{reader: r}
}

// ---------------------------------------------------------------------------
// selectTFAType
// ---------------------------------------------------------------------------

func TestSelectTFAType_EmptyTypes(t *testing.T) {
	t.Parallel()

	// selectTFAType returns immediately for empty types — reader is unused.
	h := newInteractiveTFAHandlerWithReader(bufio.NewReader(strings.NewReader("")))

	got := h.selectTFAType([]string{})
	if got != string(TFATypeTOTP) {
		t.Errorf("selectTFAType([]) = %q, want %q", got, TFATypeTOTP)
	}
}

func TestSelectTFAType_SingleType(t *testing.T) {
	t.Parallel()

	h := newInteractiveTFAHandlerWithReader(bufio.NewReader(strings.NewReader("")))

	got := h.selectTFAType([]string{string(TFATypeYubico)})
	if got != string(TFATypeYubico) {
		t.Errorf("selectTFAType([yubico]) = %q, want %q", got, TFATypeYubico)
	}
}

func TestSelectTFAType_MultipleTypes_NumericChoice(t *testing.T) {
	t.Parallel()

	// Reader provides "2\n" — selecting the second option.
	h := newInteractiveTFAHandlerWithReader(bufio.NewReader(strings.NewReader("2\n")))

	got := h.selectTFAType([]string{string(TFATypeTOTP), string(TFATypeRecovery), string(TFATypeYubico)})
	if got != string(TFATypeRecovery) {
		t.Errorf("selectTFAType(numeric 2) = %q, want %q", got, TFATypeRecovery)
	}
}

func TestSelectTFAType_MultipleTypes_NameChoice(t *testing.T) {
	t.Parallel()

	h := newInteractiveTFAHandlerWithReader(bufio.NewReader(strings.NewReader(string(TFATypeTOTP) + "\n")))

	got := h.selectTFAType([]string{string(TFATypeTOTP), string(TFATypeRecovery)})
	if got != string(TFATypeTOTP) {
		t.Errorf("selectTFAType(name) = %q, want %q", got, TFATypeTOTP)
	}
}

func TestSelectTFAType_MultipleTypes_InvalidThenValid(t *testing.T) {
	t.Parallel()

	// First line invalid, second valid.
	h := newInteractiveTFAHandlerWithReader(bufio.NewReader(strings.NewReader("invalid\n1\n")))

	got := h.selectTFAType([]string{string(TFATypeTOTP), string(TFATypeRecovery)})
	if got != string(TFATypeTOTP) {
		t.Errorf("selectTFAType(invalid then 1) = %q, want %q", got, TFATypeTOTP)
	}
}

func TestSelectTFAType_MultipleTypes_CaseInsensitiveName(t *testing.T) {
	t.Parallel()

	// EqualFold comparison — uppercase should match.
	h := newInteractiveTFAHandlerWithReader(bufio.NewReader(strings.NewReader("RECOVERY\n")))

	got := h.selectTFAType([]string{string(TFATypeTOTP), string(TFATypeRecovery)})
	if got != string(TFATypeRecovery) {
		t.Errorf("selectTFAType(RECOVERY) = %q, want %q", got, TFATypeRecovery)
	}
}

func TestSelectTFAType_MultipleTypes_FirstValidAfterEOF(t *testing.T) {
	t.Parallel()

	// Reader provides valid numeric choice immediately.
	h := newInteractiveTFAHandlerWithReader(bufio.NewReader(strings.NewReader("1\n")))

	got := h.selectTFAType([]string{string(TFATypeTOTP), string(TFATypeYubico)})
	if got != string(TFATypeTOTP) {
		t.Errorf("selectTFAType = %q, want %q", got, TFATypeTOTP)
	}
}

// ---------------------------------------------------------------------------
// promptTOTP
// ---------------------------------------------------------------------------

func TestPromptTOTP(t *testing.T) {
	t.Parallel()

	h := newInteractiveTFAHandlerWithReader(bufio.NewReader(strings.NewReader("654321\n")))

	code, err := h.promptTOTP()
	if err != nil {
		t.Fatalf("promptTOTP() error = %v", err)
	}

	if code != "654321" {
		t.Errorf("promptTOTP() = %q, want %q", code, "654321")
	}
}

// ---------------------------------------------------------------------------
// promptYubico
// ---------------------------------------------------------------------------

func TestPromptYubico(t *testing.T) {
	t.Parallel()

	h := newInteractiveTFAHandlerWithReader(bufio.NewReader(strings.NewReader("ccccccabcdef\n")))

	otp, err := h.promptYubico()
	if err != nil {
		t.Fatalf("promptYubico() error = %v", err)
	}

	if otp != "ccccccabcdef" {
		t.Errorf("promptYubico() = %q, want %q", otp, "ccccccabcdef")
	}
}

// ---------------------------------------------------------------------------
// promptRecovery
// ---------------------------------------------------------------------------

func TestPromptRecovery(t *testing.T) {
	t.Parallel()

	h := newInteractiveTFAHandlerWithReader(bufio.NewReader(strings.NewReader("abc-def-ghi\n")))

	code, err := h.promptRecovery()
	if err != nil {
		t.Fatalf("promptRecovery() error = %v", err)
	}

	if code != "abc-def-ghi" {
		t.Errorf("promptRecovery() = %q, want %q", code, "abc-def-ghi")
	}
}

// ---------------------------------------------------------------------------
// promptGeneric
// ---------------------------------------------------------------------------

func TestPromptGeneric(t *testing.T) {
	t.Parallel()

	h := newInteractiveTFAHandlerWithReader(bufio.NewReader(strings.NewReader("mycode\n")))

	code, err := h.promptGeneric("customtype")
	if err != nil {
		t.Fatalf("promptGeneric() error = %v", err)
	}

	if code != "mycode" {
		t.Errorf("promptGeneric() = %q, want %q", code, "mycode")
	}
}

// ---------------------------------------------------------------------------
// HandleTFAChallenge (InteractiveTFAHandler)
// ---------------------------------------------------------------------------

func TestInteractiveTFAHandler_HandleTFAChallenge_TOTP(t *testing.T) {
	t.Parallel()

	h := newInteractiveTFAHandlerWithReader(bufio.NewReader(strings.NewReader("123456\n")))
	challenge := &TFAChallenge{Types: []string{string(TFATypeTOTP)}}

	resp, err := h.HandleTFAChallenge(challenge)
	if err != nil {
		t.Fatalf("HandleTFAChallenge() error = %v", err)
	}

	if resp.Response != "123456" {
		t.Errorf("Response = %q, want %q", resp.Response, "123456")
	}

	if resp.Type != string(TFATypeTOTP) {
		t.Errorf("Type = %q, want %q", resp.Type, TFATypeTOTP)
	}
}

func TestInteractiveTFAHandler_HandleTFAChallenge_Yubico(t *testing.T) {
	t.Parallel()

	h := newInteractiveTFAHandlerWithReader(bufio.NewReader(strings.NewReader("ccccccabcdef\n")))
	challenge := &TFAChallenge{Types: []string{string(TFATypeYubico)}}

	resp, err := h.HandleTFAChallenge(challenge)
	if err != nil {
		t.Fatalf("HandleTFAChallenge(yubico) error = %v", err)
	}

	if resp.Response != "ccccccabcdef" {
		t.Errorf("Response = %q, want %q", resp.Response, "ccccccabcdef")
	}
}

func TestInteractiveTFAHandler_HandleTFAChallenge_Recovery(t *testing.T) {
	t.Parallel()

	h := newInteractiveTFAHandlerWithReader(bufio.NewReader(strings.NewReader("recovery-abc\n")))
	challenge := &TFAChallenge{Types: []string{string(TFATypeRecovery)}}

	resp, err := h.HandleTFAChallenge(challenge)
	if err != nil {
		t.Fatalf("HandleTFAChallenge(recovery) error = %v", err)
	}

	if resp.Response != "recovery-abc" {
		t.Errorf("Response = %q, want %q", resp.Response, "recovery-abc")
	}
}

func TestInteractiveTFAHandler_HandleTFAChallenge_U2F_ReturnsError(t *testing.T) {
	t.Parallel()

	h := newInteractiveTFAHandlerWithReader(bufio.NewReader(strings.NewReader("")))
	challenge := &TFAChallenge{Types: []string{string(TFATypeU2F)}}

	_, err := h.HandleTFAChallenge(challenge)
	if err == nil {
		t.Fatal("expected error for U2F challenge, got nil")
	}

	if !errors.Is(err, ErrHardwareTokenRequiresBrowser) {
		t.Errorf("error = %v, want ErrHardwareTokenRequiresBrowser", err)
	}
}

func TestInteractiveTFAHandler_HandleTFAChallenge_WebAuthn_ReturnsError(t *testing.T) {
	t.Parallel()

	h := newInteractiveTFAHandlerWithReader(bufio.NewReader(strings.NewReader("")))
	challenge := &TFAChallenge{Types: []string{string(TFATypeWebAuthn)}}

	_, err := h.HandleTFAChallenge(challenge)
	if err == nil {
		t.Fatal("expected error for WebAuthn challenge, got nil")
	}

	if !errors.Is(err, ErrHardwareTokenRequiresBrowser) {
		t.Errorf("error = %v, want ErrHardwareTokenRequiresBrowser", err)
	}
}

func TestInteractiveTFAHandler_HandleTFAChallenge_NoTypes_DefaultTOTP(t *testing.T) {
	t.Parallel()

	// No types specified → selectTFAType returns TOTP → promptTOTP.
	h := newInteractiveTFAHandlerWithReader(bufio.NewReader(strings.NewReader("999888\n")))
	challenge := &TFAChallenge{Types: nil}

	resp, err := h.HandleTFAChallenge(challenge)
	if err != nil {
		t.Fatalf("HandleTFAChallenge(no types) error = %v", err)
	}

	if resp.Response != "999888" {
		t.Errorf("Response = %q, want %q", resp.Response, "999888")
	}
}

func TestInteractiveTFAHandler_HandleTFAChallenge_Generic(t *testing.T) {
	t.Parallel()

	h := newInteractiveTFAHandlerWithReader(bufio.NewReader(strings.NewReader("generic-code\n")))
	challenge := &TFAChallenge{Types: []string{"custom"}}

	resp, err := h.HandleTFAChallenge(challenge)
	if err != nil {
		t.Fatalf("HandleTFAChallenge(generic) error = %v", err)
	}

	if resp.Response != "generic-code" {
		t.Errorf("Response = %q, want %q", resp.Response, "generic-code")
	}
}

func TestNewInteractiveTFAHandler(t *testing.T) {
	t.Parallel()

	h := NewInteractiveTFAHandler()
	if h == nil {
		t.Fatal("NewInteractiveTFAHandler() returned nil")
	}

	if h.reader == nil {
		t.Error("NewInteractiveTFAHandler().reader is nil")
	}
}
