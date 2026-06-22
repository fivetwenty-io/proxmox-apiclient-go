package ssl_test

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/fivetwenty-io/pve-apiclient-go/v3/internal/ssl"
)

const (
	covFP1 = "AA:BB:CC:DD:EE:FF:00:11:22:33:44:55:66:77:88:99:AA:BB:CC:DD:EE:FF:00:11:22:33:44:55:66:77:88:99"
	covFP2 = "11:22:33:44:55:66:77:88:99:AA:BB:CC:DD:EE:FF:00:11:22:33:44:55:66:77:88:99:AA:BB:CC:DD:EE:FF:00"

	covExtKeyUsageStr = "Usage"
)

// covMintCert mints a minimal self-signed ECDSA certificate.
// Callers may pass non-nil opts to overlay template fields.
func covMintCert(tb testing.TB, opts *x509.Certificate) (*x509.Certificate, *ecdsa.PrivateKey) {
	tb.Helper()

	privKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		tb.Fatalf("generate key: %v", err)
	}

	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: "test"},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(time.Hour),
	}

	if opts != nil {
		if opts.SerialNumber != nil {
			tmpl.SerialNumber = opts.SerialNumber
		}

		if opts.Subject.CommonName != "" {
			tmpl.Subject = opts.Subject
		}

		if !opts.NotBefore.IsZero() {
			tmpl.NotBefore = opts.NotBefore
		}

		if !opts.NotAfter.IsZero() {
			tmpl.NotAfter = opts.NotAfter
		}

		tmpl.DNSNames = opts.DNSNames
		tmpl.IPAddresses = opts.IPAddresses
		tmpl.KeyUsage = opts.KeyUsage
		tmpl.ExtKeyUsage = opts.ExtKeyUsage
	}

	derBytes, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &privKey.PublicKey, privKey)
	if err != nil {
		tb.Fatalf("create certificate: %v", err)
	}

	parsed, err := x509.ParseCertificate(derBytes)
	if err != nil {
		tb.Fatalf("parse certificate: %v", err)
	}

	return parsed, privKey
}

// covWritePEM writes a PEM-encoded certificate to dir and returns its path.
func covWritePEM(tb testing.TB, dir string, cert *x509.Certificate) string {
	tb.Helper()

	path := filepath.Join(dir, "ca.pem")
	block := &pem.Block{Type: "CERTIFICATE", Bytes: cert.Raw}

	err := os.WriteFile(path, pem.EncodeToMemory(block), 0o600)
	if err != nil {
		tb.Fatalf("write PEM: %v", err)
	}

	return path
}

// covWriteKeyPair writes cert + key PEM files and returns (certPath, keyPath).
func covWriteKeyPair(tb testing.TB, dir string, cert *x509.Certificate, privKey *ecdsa.PrivateKey) (string, string) {
	tb.Helper()

	certPath := filepath.Join(dir, "client.crt")
	keyPath := filepath.Join(dir, "client.key")

	certBlock := &pem.Block{Type: "CERTIFICATE", Bytes: cert.Raw}

	err := os.WriteFile(certPath, pem.EncodeToMemory(certBlock), 0o600)
	if err != nil {
		tb.Fatalf("write cert PEM: %v", err)
	}

	keyDER, err := x509.MarshalECPrivateKey(privKey)
	if err != nil {
		tb.Fatalf("marshal key: %v", err)
	}

	keyBlock := &pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER}

	err = os.WriteFile(keyPath, pem.EncodeToMemory(keyBlock), 0o600)
	if err != nil {
		tb.Fatalf("write key PEM: %v", err)
	}

	return certPath, keyPath
}

// ---------------------------------------------------------------------------
// FingerprintCache tests
// ---------------------------------------------------------------------------

func TestCovNewFingerprintCache(t *testing.T) {
	t.Parallel()

	cache := ssl.NewFingerprintCache("")
	if cache == nil {
		t.Fatal("NewFingerprintCache returned nil")
	}

	entries := cache.GetAll()
	if len(entries) != 0 {
		t.Fatalf("expected 0 entries, got %d", len(entries))
	}
}

func TestCovFingerprintCache_LoadMissingFile(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	cache := ssl.NewFingerprintCache(filepath.Join(dir, "nonexistent.json"))

	err := cache.Load()
	if err != nil {
		t.Fatalf("Load missing file should return nil, got %v", err)
	}
}

func TestCovFingerprintCache_LoadMalformedJSON(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	path := filepath.Join(dir, "bad.json")

	err := os.WriteFile(path, []byte("{bad json"), 0o600)
	if err != nil {
		t.Fatalf("write file: %v", err)
	}

	cache := ssl.NewFingerprintCache(path)

	loadErr := cache.Load()
	if loadErr == nil {
		t.Fatal("Load malformed JSON should return error")
	}
}

func TestCovFingerprintCache_LoadEmptyFilename(t *testing.T) {
	t.Parallel()

	cache := ssl.NewFingerprintCache("")

	err := cache.Load()
	if err != nil {
		t.Fatalf("Load with empty filename should return nil, got %v", err)
	}
}

func TestCovFingerprintCache_SaveAndLoad(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	path := filepath.Join(dir, "cache.json")

	cache := ssl.NewFingerprintCache(path)
	cache.SetAutoSave(false)

	entry := ssl.FingerprintEntry{
		Fingerprint: covFP1,
		Host:        "pve.example.com",
		Port:        8006,
		Trusted:     true,
	}

	err := cache.Add(entry)
	if err != nil {
		t.Fatalf("Add: %v", err)
	}

	err = cache.Save()
	if err != nil {
		t.Fatalf("Save: %v", err)
	}

	cache2 := ssl.NewFingerprintCache(path)

	err = cache2.Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	loaded := cache2.GetAll()
	if len(loaded) != 1 {
		t.Fatalf("expected 1 entry after load, got %d", len(loaded))
	}
}

func TestCovFingerprintCache_SaveEmptyFilename(t *testing.T) {
	t.Parallel()

	cache := ssl.NewFingerprintCache("")
	cache.SetAutoSave(false)

	err := cache.Save()
	if err != nil {
		t.Fatalf("Save with empty filename should return nil, got %v", err)
	}
}

func TestCovFingerprintCache_AddNormalizesFingerprint(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	cache := ssl.NewFingerprintCache(filepath.Join(dir, "cache.json"))
	cache.SetAutoSave(false)

	lower := "aa:bb:cc:dd:ee:ff:00:11:22:33:44:55:66:77:88:99:aa:bb:cc:dd:ee:ff:00:11:22:33:44:55:66:77:88:99"

	err := cache.Add(ssl.FingerprintEntry{Fingerprint: lower, Host: "h1", Port: 1})
	if err != nil {
		t.Fatalf("Add lower: %v", err)
	}

	got, ok := cache.Get(covFP1)
	if !ok {
		t.Fatal("Get with upper-case key should find lower-case entry")
	}

	if got.Host != "h1" {
		t.Fatalf("expected host h1, got %s", got.Host)
	}
}

func TestCovFingerprintCache_AddPreservesFirstSeen(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	cache := ssl.NewFingerprintCache(filepath.Join(dir, "cache.json"))
	cache.SetAutoSave(false)

	err := cache.Add(ssl.FingerprintEntry{Fingerprint: covFP1, Host: "h1", Port: 1})
	if err != nil {
		t.Fatalf("first Add: %v", err)
	}

	first, _ := cache.Get(covFP1)
	firstSeen := first.FirstSeen

	// Add sets FirstSeen on creation; re-add must preserve it.
	err = cache.Add(ssl.FingerprintEntry{Fingerprint: covFP1, Host: "h1", Port: 1})
	if err != nil {
		t.Fatalf("second Add: %v", err)
	}

	second, _ := cache.Get(covFP1)
	if !second.FirstSeen.Equal(firstSeen) {
		t.Fatalf("FirstSeen changed on update: was %v, now %v", firstSeen, second.FirstSeen)
	}
}

func TestCovFingerprintCache_Get_Miss(t *testing.T) {
	t.Parallel()

	cache := ssl.NewFingerprintCache("")
	_, ok := cache.Get(covFP1)

	if ok {
		t.Fatal("Get on empty cache should return false")
	}
}

func TestCovFingerprintCache_Remove(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	cache := ssl.NewFingerprintCache(filepath.Join(dir, "cache.json"))
	cache.SetAutoSave(false)

	err := cache.Add(ssl.FingerprintEntry{Fingerprint: covFP1, Host: "h", Port: 1})
	if err != nil {
		t.Fatalf("Add: %v", err)
	}

	err = cache.Remove(covFP1)
	if err != nil {
		t.Fatalf("Remove: %v", err)
	}

	_, ok := cache.Get(covFP1)
	if ok {
		t.Fatal("entry still present after Remove")
	}
}

func TestCovFingerprintCache_GetTrusted(t *testing.T) {
	t.Parallel()

	cache := ssl.NewFingerprintCache("")
	cache.SetAutoSave(false)

	err := cache.Add(ssl.FingerprintEntry{Fingerprint: covFP1, Host: "h1", Trusted: true})
	if err != nil {
		t.Fatalf("Add fp1: %v", err)
	}

	err = cache.Add(ssl.FingerprintEntry{Fingerprint: covFP2, Host: "h2", Trusted: false})
	if err != nil {
		t.Fatalf("Add fp2: %v", err)
	}

	trusted := cache.GetTrusted()
	if len(trusted) != 1 {
		t.Fatalf("expected 1 trusted, got %d", len(trusted))
	}

	if trusted[0].Host != "h1" {
		t.Fatalf("expected host h1, got %s", trusted[0].Host)
	}
}

func TestCovFingerprintCache_GetByHost(t *testing.T) {
	t.Parallel()

	cache := ssl.NewFingerprintCache("")
	cache.SetAutoSave(false)

	err := cache.Add(ssl.FingerprintEntry{Fingerprint: covFP1, Host: "pve1", Port: 8006})
	if err != nil {
		t.Fatalf("Add fp1: %v", err)
	}

	err = cache.Add(ssl.FingerprintEntry{Fingerprint: covFP2, Host: "pve2", Port: 8006})
	if err != nil {
		t.Fatalf("Add fp2: %v", err)
	}

	entries := cache.GetByHost("pve1")
	if len(entries) != 1 {
		t.Fatalf("expected 1 entry for pve1, got %d", len(entries))
	}
}

func TestCovFingerprintCache_SetTrusted(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	cache := ssl.NewFingerprintCache(filepath.Join(dir, "cache.json"))
	cache.SetAutoSave(false)

	err := cache.Add(ssl.FingerprintEntry{Fingerprint: covFP1, Host: "h", Trusted: false})
	if err != nil {
		t.Fatalf("Add: %v", err)
	}

	err = cache.SetTrusted(covFP1, true)
	if err != nil {
		t.Fatalf("SetTrusted: %v", err)
	}

	entry, ok := cache.Get(covFP1)
	if !ok {
		t.Fatal("entry not found after SetTrusted")
	}

	if !entry.Trusted {
		t.Fatal("expected Trusted=true after SetTrusted")
	}
}

func TestCovFingerprintCache_SetTrustedMissing(t *testing.T) {
	t.Parallel()

	cache := ssl.NewFingerprintCache("")
	cache.SetAutoSave(false)

	// SetTrusted on non-existent key should be a no-op, not an error.
	err := cache.SetTrusted(covFP1, true)
	if err != nil {
		t.Fatalf("SetTrusted on missing key returned error: %v", err)
	}
}

func TestCovFingerprintCache_Clear(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	cache := ssl.NewFingerprintCache(filepath.Join(dir, "cache.json"))
	cache.SetAutoSave(false)

	err := cache.Add(ssl.FingerprintEntry{Fingerprint: covFP1, Host: "h"})
	if err != nil {
		t.Fatalf("Add: %v", err)
	}

	err = cache.Clear()
	if err != nil {
		t.Fatalf("Clear: %v", err)
	}

	if len(cache.GetAll()) != 0 {
		t.Fatal("cache not empty after Clear")
	}
}

func TestCovFingerprintCache_AutoSaveOff(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	path := filepath.Join(dir, "cache.json")
	cache := ssl.NewFingerprintCache(path)
	cache.SetAutoSave(false)

	err := cache.Add(ssl.FingerprintEntry{Fingerprint: covFP1, Host: "h"})
	if err != nil {
		t.Fatalf("Add: %v", err)
	}

	// File must not exist because autoSave is disabled.
	_, statErr := os.Stat(path)
	if !os.IsNotExist(statErr) {
		t.Fatal("cache file written even though autoSave is false")
	}
}

func TestCovFingerprintCache_AutoSaveOn(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	path := filepath.Join(dir, "sub", "cache.json")
	cache := ssl.NewFingerprintCache(path)

	err := cache.Add(ssl.FingerprintEntry{Fingerprint: covFP1, Host: "h"})
	if err != nil {
		t.Fatalf("Add with autoSave: %v", err)
	}

	_, statErr := os.Stat(path)
	if statErr != nil {
		t.Fatalf("cache file not written by autoSave: %v", statErr)
	}
}

func TestCovFingerprintCache_CleanupExpiredZeroNotAfter(t *testing.T) {
	t.Parallel()

	cache := ssl.NewFingerprintCache("")
	cache.SetAutoSave(false)

	// NotAfter is zero in FingerprintEntry default → not treated as expired.
	err := cache.Add(ssl.FingerprintEntry{Fingerprint: covFP1, Host: "h"})
	if err != nil {
		t.Fatalf("Add: %v", err)
	}

	err = cache.CleanupExpired()
	if err != nil {
		t.Fatalf("CleanupExpired: %v", err)
	}

	if len(cache.GetAll()) != 1 {
		t.Fatal("expected entry to remain when NotAfter is zero")
	}
}

func TestCovFingerprintCache_CleanupExpiredWithNotAfter(t *testing.T) {
	t.Parallel()

	// Build a cache JSON directly so we can set NotAfter/LastUsed freely.
	dir := t.TempDir()
	path := filepath.Join(dir, "c.json")

	const rawJSON = `{
  "AA:BB:CC:DD:EE:FF:00:11:22:33:44:55:66:77:88:99:AA:BB:CC:DD:EE:FF:00:11:22:33:44:55:66:77:88:99": {
    "fingerprint": "AA:BB:CC:DD:EE:FF:00:11:22:33:44:55:66:77:88:99:AA:BB:CC:DD:EE:FF:00:11:22:33:44:55:66:77:88:99",
    "host": "h",
    "port": 8006,
    "trusted": false,
    "first_seen": "2020-01-01T00:00:00Z",
    "last_seen":  "2020-01-01T00:00:00Z",
    "last_used":  "2020-01-01T00:00:00Z",
    "not_after":  "2020-01-01T00:00:00Z"
  }
}`

	err := os.WriteFile(path, []byte(rawJSON), 0o600)
	if err != nil {
		t.Fatalf("write fixture: %v", err)
	}

	cache := ssl.NewFingerprintCache(path)
	cache.SetAutoSave(false)

	err = cache.Load()
	if err != nil {
		t.Fatalf("Load fixture: %v", err)
	}

	err = cache.CleanupExpired()
	if err != nil {
		t.Fatalf("CleanupExpired: %v", err)
	}

	if len(cache.GetAll()) != 0 {
		t.Fatal("expired entry not removed by CleanupExpired")
	}
}

func TestCovFingerprintCache_CleanupExpiredRecentlyUsed(t *testing.T) {
	t.Parallel()

	// Entry is expired but used recently → must be kept.
	dir := t.TempDir()
	path := filepath.Join(dir, "c.json")

	lastUsed := time.Now().UTC().Format(time.RFC3339)
	notAfter := time.Now().Add(-time.Hour).UTC().Format(time.RFC3339)

	rawJSON := `{
  "AA:BB:CC:DD:EE:FF:00:11:22:33:44:55:66:77:88:99:AA:BB:CC:DD:EE:FF:00:11:22:33:44:55:66:77:88:99": {
    "fingerprint": "AA:BB:CC:DD:EE:FF:00:11:22:33:44:55:66:77:88:99:AA:BB:CC:DD:EE:FF:00:11:22:33:44:55:66:77:88:99",
    "host": "h",
    "port": 8006,
    "trusted": false,
    "first_seen": "2020-01-01T00:00:00Z",
    "last_seen": "2020-01-01T00:00:00Z",
    "last_used": "` + lastUsed + `",
    "not_after": "` + notAfter + `"
  }
}`

	err := os.WriteFile(path, []byte(rawJSON), 0o600)
	if err != nil {
		t.Fatalf("write fixture: %v", err)
	}

	cache := ssl.NewFingerprintCache(path)
	cache.SetAutoSave(false)

	err = cache.Load()
	if err != nil {
		t.Fatalf("Load fixture: %v", err)
	}

	err = cache.CleanupExpired()
	if err != nil {
		t.Fatalf("CleanupExpired: %v", err)
	}

	if len(cache.GetAll()) != 1 {
		t.Fatal("recently-used expired entry should not be removed")
	}
}

func TestCovGetDefaultCacheFile(t *testing.T) {
	t.Parallel()

	cacheFile := ssl.GetDefaultCacheFile()
	// Must end in fingerprints.json or be empty (when UserHomeDir fails).
	if cacheFile != "" && !strings.HasSuffix(cacheFile, "fingerprints.json") {
		t.Fatalf("unexpected default cache file path: %s", cacheFile)
	}
}

// ---------------------------------------------------------------------------
// CalculateFingerprint — golden SHA256 test
// ---------------------------------------------------------------------------

func TestCovCalculateFingerprint(t *testing.T) {
	t.Parallel()

	cert, _ := covMintCert(t, nil)
	fingerprint := ssl.CalculateFingerprint(cert)

	// Must be uppercase colon-hex of 32 bytes = 95 chars (32*2 + 31 colons).
	const wantLen = 95
	if len(fingerprint) != wantLen {
		t.Fatalf("fingerprint length = %d, want %d", len(fingerprint), wantLen)
	}

	if fingerprint != strings.ToUpper(fingerprint) {
		t.Fatal("fingerprint must be uppercase")
	}

	for pos, runeChar := range fingerprint {
		expectColon := (pos%3 == 2)
		isColon := runeChar == ':'
		isHex := (runeChar >= '0' && runeChar <= '9') || (runeChar >= 'A' && runeChar <= 'F')

		if expectColon && !isColon {
			t.Fatalf("expected ':' at position %d, got %c", pos, runeChar)
		}

		if !expectColon && !isHex {
			t.Fatalf("expected hex at position %d, got %c", pos, runeChar)
		}
	}
}

// ---------------------------------------------------------------------------
// FingerprintVerifier coverage tests
// ---------------------------------------------------------------------------

func TestCovVerifyCertificate_Nil(t *testing.T) {
	t.Parallel()

	verifier := ssl.NewFingerprintVerifier()

	err := verifier.VerifyCertificate(nil)
	if err == nil {
		t.Fatal("expected error for nil cert")
	}
}

func TestCovVerifyCertificate_CachedTrusted(t *testing.T) {
	t.Parallel()

	cert, _ := covMintCert(t, nil)
	fingerprint := ssl.CalculateFingerprint(cert)

	verifier := ssl.NewFingerprintVerifier()
	verifier.AddTrustedFingerprint(fingerprint)

	err := verifier.VerifyCertificate(cert)
	if err != nil {
		t.Fatalf("trusted cert should verify: %v", err)
	}
}

func TestCovVerifyCertificate_CachedUntrusted(t *testing.T) {
	t.Parallel()

	cert, _ := covMintCert(t, nil)

	verifier := ssl.NewFingerprintVerifier()
	// Populate cache as untrusted via rejecting callback.
	verifier.SetVerifyCallback(func(_ *x509.Certificate) bool { return false })

	err := verifier.VerifyCertificate(cert)
	if err == nil {
		t.Fatal("expected error for callback-rejected cert")
	}

	// Second verify: fingerprint is cached as false, callback path should not fire again.
	verifier.SetVerifyCallback(nil)

	err = verifier.VerifyCertificate(cert)
	if err == nil {
		t.Fatal("expected error for cached-untrusted cert")
	}
}

func TestCovVerifyCertificate_CallbackTrue(t *testing.T) {
	t.Parallel()

	cert, _ := covMintCert(t, nil)

	verifier := ssl.NewFingerprintVerifier()

	var registered string

	verifier.SetRegisterCallback(func(fp string) { registered = fp })
	verifier.SetVerifyCallback(func(_ *x509.Certificate) bool { return true })

	err := verifier.VerifyCertificate(cert)
	if err != nil {
		t.Fatalf("callback=true should pass: %v", err)
	}

	expected := ssl.CalculateFingerprint(cert)
	if registered != expected {
		t.Fatalf("register callback got %q, want %q", registered, expected)
	}
}

func TestCovVerifyCertificate_CallbackFalse(t *testing.T) {
	t.Parallel()

	cert, _ := covMintCert(t, nil)

	verifier := ssl.NewFingerprintVerifier()
	verifier.SetVerifyCallback(func(_ *x509.Certificate) bool { return false })

	err := verifier.VerifyCertificate(cert)
	if err == nil {
		t.Fatal("expected error when callback returns false")
	}
}

func TestCovVerifyCertificate_ManualMode(t *testing.T) {
	t.Parallel()

	cert, _ := covMintCert(t, nil)

	verifier := ssl.NewFingerprintVerifier()
	verifier.SetManualVerification(true)

	err := verifier.VerifyCertificate(cert)
	if err == nil {
		t.Fatal("manual mode with unknown cert should return error")
	}
}

func TestCovVerifyCertificate_NoMethod(t *testing.T) {
	t.Parallel()

	cert, _ := covMintCert(t, nil)

	verifier := ssl.NewFingerprintVerifier()
	// No callback, no manual mode — should reject.
	err := verifier.VerifyCertificate(cert)
	if err == nil {
		t.Fatal("no-method should return error")
	}
}

func TestCovGetLastUnknownFingerprint(t *testing.T) {
	t.Parallel()

	cert, _ := covMintCert(t, nil)
	expectedFP := ssl.CalculateFingerprint(cert)

	verifier := ssl.NewFingerprintVerifier()
	// Trigger an unknown encounter.
	_ = verifier.VerifyCertificate(cert)

	last := verifier.GetLastUnknownFingerprint()
	if last != expectedFP {
		t.Fatalf("GetLastUnknownFingerprint = %q, want %q", last, expectedFP)
	}
}

// ---------------------------------------------------------------------------
// CreateTLSConfig
// ---------------------------------------------------------------------------

func TestCovCreateTLSConfig_NilOptions(t *testing.T) {
	t.Parallel()

	cfg, err := ssl.CreateTLSConfig("pve.example.com:8006", nil)
	if err != nil {
		t.Fatalf("nil options: %v", err)
	}

	if cfg == nil {
		t.Fatal("expected non-nil config")
	}

	if cfg.ServerName != "pve.example.com" {
		t.Fatalf("ServerName = %q, want pve.example.com", cfg.ServerName)
	}
}

func TestCovCreateTLSConfig_InsecureSkipVerify(t *testing.T) {
	t.Parallel()

	opts := &ssl.TLSOptions{InsecureSkipVerify: true}

	cfg, err := ssl.CreateTLSConfig("host", opts)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if !cfg.InsecureSkipVerify {
		t.Fatal("InsecureSkipVerify should be true")
	}
}

func TestCovCreateTLSConfig_CACertLoadError(t *testing.T) {
	t.Parallel()

	opts := &ssl.TLSOptions{CACert: "/nonexistent/path/ca.pem"}

	_, err := ssl.CreateTLSConfig("host", opts)
	if err == nil {
		t.Fatal("expected error for missing CA file")
	}
}

func TestCovCreateTLSConfig_ClientCertError(t *testing.T) {
	t.Parallel()

	opts := &ssl.TLSOptions{
		ClientCert: "/nonexistent/client.crt",
		ClientKey:  "/nonexistent/client.key",
	}

	_, err := ssl.CreateTLSConfig("host", opts)
	if err == nil {
		t.Fatal("expected error for missing client cert")
	}
}

func TestCovCreateTLSConfig_ValidClientCert(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	cert, privKey := covMintCert(t, nil)
	certPath, keyPath := covWriteKeyPair(t, dir, cert, privKey)

	opts := &ssl.TLSOptions{ClientCert: certPath, ClientKey: keyPath}

	cfg, err := ssl.CreateTLSConfig("host", opts)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(cfg.Certificates) != 1 {
		t.Fatalf("expected 1 client certificate, got %d", len(cfg.Certificates))
	}
}

func TestCovCreateTLSConfig_MinTLSVersion(t *testing.T) {
	t.Parallel()

	opts := &ssl.TLSOptions{MinTLSVersion: tls.VersionTLS13}

	cfg, err := ssl.CreateTLSConfig("host", opts)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if cfg.MinVersion != tls.VersionTLS13 {
		t.Fatalf("MinVersion = %d, want %d", cfg.MinVersion, tls.VersionTLS13)
	}
}

func TestCovCreateTLSConfig_DefaultMinTLSVersion(t *testing.T) {
	t.Parallel()

	opts := &ssl.TLSOptions{}

	cfg, err := ssl.CreateTLSConfig("host", opts)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if cfg.MinVersion != tls.VersionTLS12 {
		t.Fatalf("default MinVersion = %d, want %d", cfg.MinVersion, tls.VersionTLS12)
	}
}

func TestCovCreateTLSConfig_CipherSuites(t *testing.T) {
	t.Parallel()

	suites := []uint16{tls.TLS_AES_128_GCM_SHA256}
	opts := &ssl.TLSOptions{CipherSuites: suites}

	cfg, err := ssl.CreateTLSConfig("host", opts)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(cfg.CipherSuites) != 1 {
		t.Fatalf("CipherSuites length = %d, want 1", len(cfg.CipherSuites))
	}
}

func TestCovCreateTLSConfig_ValidCACert(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	cert, _ := covMintCert(t, nil)
	caPath := covWritePEM(t, dir, cert)

	opts := &ssl.TLSOptions{CACert: caPath}

	cfg, err := ssl.CreateTLSConfig("host", opts)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if cfg.RootCAs == nil {
		t.Fatal("expected non-nil RootCAs")
	}
}

// ---------------------------------------------------------------------------
// LoadCACertificate
// ---------------------------------------------------------------------------

func TestCovLoadCACertificate_EmptyReturnsPool(t *testing.T) {
	t.Parallel()

	pool, err := ssl.LoadCACertificate("")
	if err != nil {
		t.Fatalf("empty filename: %v", err)
	}

	if pool == nil {
		t.Fatal("expected non-nil pool for empty filename")
	}
}

func TestCovLoadCACertificate_RelativePathError(t *testing.T) {
	t.Parallel()

	_, err := ssl.LoadCACertificate("relative/path.pem")
	if err == nil {
		t.Fatal("expected error for relative path")
	}

	if !strings.Contains(err.Error(), "absolute") {
		t.Fatalf("error should mention absolute path, got: %v", err)
	}
}

func TestCovLoadCACertificate_MissingFile(t *testing.T) {
	t.Parallel()

	_, err := ssl.LoadCACertificate("/nonexistent/path/ca.pem")
	if err == nil {
		t.Fatal("expected error for missing file")
	}
}

func TestCovLoadCACertificate_BadPEM(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	path := filepath.Join(dir, "bad.pem")

	err := os.WriteFile(path, []byte("not a pem"), 0o600)
	if err != nil {
		t.Fatalf("write file: %v", err)
	}

	_, loadErr := ssl.LoadCACertificate(path)
	if loadErr == nil {
		t.Fatal("expected error for bad PEM")
	}
}

func TestCovLoadCACertificate_ValidPEM(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	cert, _ := covMintCert(t, nil)
	path := covWritePEM(t, dir, cert)

	pool, err := ssl.LoadCACertificate(path)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if pool == nil {
		t.Fatal("expected non-nil pool")
	}
}

// ---------------------------------------------------------------------------
// extractHostname (tested indirectly via CreateTLSConfig)
// ---------------------------------------------------------------------------

func TestCovExtractHostname_WithPort(t *testing.T) {
	t.Parallel()

	cfg, err := ssl.CreateTLSConfig("pve.local:8006", nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if cfg.ServerName != "pve.local" {
		t.Fatalf("ServerName = %q, want pve.local", cfg.ServerName)
	}
}

func TestCovExtractHostname_NoPort(t *testing.T) {
	t.Parallel()

	cfg, err := ssl.CreateTLSConfig("pve.local", nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if cfg.ServerName != "pve.local" {
		t.Fatalf("ServerName = %q, want pve.local", cfg.ServerName)
	}
}

func TestCovExtractHostname_IPv6(t *testing.T) {
	t.Parallel()

	cfg, err := ssl.CreateTLSConfig("[::1]:8006", nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if cfg.ServerName != "::1" {
		t.Fatalf("ServerName = %q, want ::1", cfg.ServerName)
	}
}

// ---------------------------------------------------------------------------
// GetCertificateInfo
// ---------------------------------------------------------------------------

func TestCovGetCertificateInfo_BasicFields(t *testing.T) {
	t.Parallel()

	cert, _ := covMintCert(t, &x509.Certificate{
		Subject:  pkix.Name{CommonName: "basic-test"},
		DNSNames: []string{"example.com"},
		IPAddresses: []net.IP{
			net.ParseIP("10.0.0.1"),
		},
		KeyUsage:    x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment,
		ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth, x509.ExtKeyUsageClientAuth},
	})

	info := ssl.GetCertificateInfo(cert)

	requiredKeys := []string{
		"subject", "issuer", "serial", "not_before", "not_after",
		"fingerprint", "signature_algorithm", "public_key_algorithm",
		"dns_names", "ip_addresses", "key_usage", "extended_key_usage",
	}

	for _, key := range requiredKeys {
		if _, ok := info[key]; !ok {
			t.Errorf("missing key in info: %s", key)
		}
	}

	extUsage, ok := info["extended_key_usage"].(string)
	if !ok {
		t.Fatal("extended_key_usage should be a string")
	}

	if !strings.Contains(extUsage, "Server Authentication") {
		t.Errorf("extended_key_usage missing Server Authentication: %s", extUsage)
	}

	if !strings.Contains(extUsage, "Client Authentication") {
		t.Errorf("extended_key_usage missing Client Authentication: %s", extUsage)
	}
}

func TestCovGetCertificateInfo_NoSANs(t *testing.T) {
	t.Parallel()

	cert, _ := covMintCert(t, nil)
	info := ssl.GetCertificateInfo(cert)

	if _, ok := info["dns_names"]; ok {
		t.Error("dns_names should not be present when cert has none")
	}

	if _, ok := info["ip_addresses"]; ok {
		t.Error("ip_addresses should not be present when cert has none")
	}
}

func TestCovGetCertificateInfo_NoExtKeyUsage(t *testing.T) {
	t.Parallel()

	cert, _ := covMintCert(t, &x509.Certificate{
		Subject: pkix.Name{CommonName: "no-ext"},
	})

	info := ssl.GetCertificateInfo(cert)
	if _, ok := info["extended_key_usage"]; ok {
		t.Error("extended_key_usage should be absent when cert has none")
	}
}

func TestCovGetCertificateInfo_AllExtKeyUsages(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		usage    x509.ExtKeyUsage
		contains string
	}{
		{"server_auth", x509.ExtKeyUsageServerAuth, "Server Authentication"},
		{"client_auth", x509.ExtKeyUsageClientAuth, "Client Authentication"},
		{"code_signing", x509.ExtKeyUsageCodeSigning, "Code Signing"},
		{"email_protection", x509.ExtKeyUsageEmailProtection, "Email Protection"},
		{"any", x509.ExtKeyUsageAny, covExtKeyUsageStr},
		{"ipsec_end_system", x509.ExtKeyUsageIPSECEndSystem, covExtKeyUsageStr},
		{"ipsec_tunnel", x509.ExtKeyUsageIPSECTunnel, covExtKeyUsageStr},
		{"ipsec_user", x509.ExtKeyUsageIPSECUser, covExtKeyUsageStr},
		{"timestamping", x509.ExtKeyUsageTimeStamping, covExtKeyUsageStr},
		{"ocsp_signing", x509.ExtKeyUsageOCSPSigning, covExtKeyUsageStr},
		{"ms_server_gated", x509.ExtKeyUsageMicrosoftServerGatedCrypto, covExtKeyUsageStr},
		{"ns_server_gated", x509.ExtKeyUsageNetscapeServerGatedCrypto, covExtKeyUsageStr},
		{"ms_commercial_code", x509.ExtKeyUsageMicrosoftCommercialCodeSigning, covExtKeyUsageStr},
		{"ms_kernel_code", x509.ExtKeyUsageMicrosoftKernelCodeSigning, covExtKeyUsageStr},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			cert, _ := covMintCert(t, &x509.Certificate{
				ExtKeyUsage: []x509.ExtKeyUsage{tc.usage},
			})

			info := ssl.GetCertificateInfo(cert)

			extUsage, ok := info["extended_key_usage"].(string)
			if !ok {
				t.Fatal("extended_key_usage not found")
			}

			if !strings.Contains(extUsage, tc.contains) {
				t.Errorf("extended_key_usage %q does not contain %q", extUsage, tc.contains)
			}
		})
	}
}
