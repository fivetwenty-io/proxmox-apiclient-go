package ssl_test

import (
	"testing"

	"github.com/fivetwenty-io/proxmox-apiclient-go/v3/internal/ssl"
)

const (
	testFingerprint  = "AA:BB:CC:DD:EE:FF:00:11:22:33:44:55:66:77:88:99:AA:BB:CC:DD:EE:FF:00:11:22:33:44:55:66:77:88:99"
	testFingerprint2 = "11:22:33:44:55:66:77:88:99:AA:BB:CC:DD:EE:FF:00:11:22:33:44:55:66:77:88:99:AA:BB:CC:DD:EE:FF:00"
)

func TestNewFingerprintVerifier(t *testing.T) {
	t.Parallel()

	fingerprintVerifier := ssl.NewFingerprintVerifier()
	if fingerprintVerifier == nil {
		t.Fatal("NewFingerprintVerifier returned nil")
	}

	// Test public interface - initially no trusted fingerprints
	fingerprints := fingerprintVerifier.GetTrustedFingerprints()
	if len(fingerprints) != 0 {
		t.Error("NewFingerprintVerifier() should start with no trusted fingerprints")
	}
}

func TestFingerprintVerifier_AddTrustedFingerprint(t *testing.T) {
	t.Parallel()

	fingerprintVerifier := ssl.NewFingerprintVerifier()

	fingerprint := testFingerprint
	fingerprintVerifier.AddTrustedFingerprint(fingerprint)

	trustedFingerprints := fingerprintVerifier.GetTrustedFingerprints()
	if len(trustedFingerprints) != 1 {
		t.Errorf("AddTrustedFingerprint() expected 1 trusted fingerprint, got %d", len(trustedFingerprints))
	}
}

func TestFingerprintVerifier_AddTrustedFingerprints(t *testing.T) {
	t.Parallel()

	fingerprintVerifier := ssl.NewFingerprintVerifier()

	fingerprints := []string{
		testFingerprint,
		testFingerprint2,
	}

	fingerprintVerifier.AddTrustedFingerprints(fingerprints)

	// Check that the fingerprints were added by retrieving them
	trustedFingerprints := fingerprintVerifier.GetTrustedFingerprints()
	if len(trustedFingerprints) != len(fingerprints) {
		t.Errorf("AddTrustedFingerprints() failed: expected %d fingerprints, got %d", len(fingerprints), len(trustedFingerprints))
	}

	// Verify each fingerprint is in the list
	for _, fingerprint := range fingerprints {
		normalized := ssl.NormalizeFingerprint(fingerprint)
		found := false

		for _, trusted := range trustedFingerprints {
			if trusted == normalized {
				found = true

				break
			}
		}

		if !found {
			t.Errorf("AddTrustedFingerprints() failed to add fingerprint %s", fingerprint)
		}
	}
}

func TestFingerprintVerifier_RemoveTrustedFingerprint(t *testing.T) {
	t.Parallel()

	fingerprintVerifier := ssl.NewFingerprintVerifier()

	fingerprint := testFingerprint
	fingerprintVerifier.AddTrustedFingerprint(fingerprint)

	// Verify it was added
	trustedFingerprints := fingerprintVerifier.GetTrustedFingerprints()
	if len(trustedFingerprints) != 1 {
		t.Fatal("Fingerprint was not added to cache")
	}

	// Remove it
	fingerprintVerifier.RemoveTrustedFingerprint(fingerprint)

	// Verify it was removed
	trustedFingerprints = fingerprintVerifier.GetTrustedFingerprints()
	if len(trustedFingerprints) != 0 {
		t.Errorf("RemoveTrustedFingerprint() failed to remove fingerprint from cache")
	}
}

func TestFingerprintVerifier_GetTrustedFingerprints(t *testing.T) {
	t.Parallel()

	fingerprintVerifier := ssl.NewFingerprintVerifier()

	fingerprints := []string{
		testFingerprint,
		testFingerprint2,
	}

	fingerprintVerifier.AddTrustedFingerprints(fingerprints)

	trusted := fingerprintVerifier.GetTrustedFingerprints()
	if len(trusted) != len(fingerprints) {
		t.Errorf("GetTrustedFingerprints() returned %d fingerprints, want %d", len(trusted), len(fingerprints))
	}
}

func TestFingerprintVerifier_ClearCache(t *testing.T) {
	t.Parallel()

	fingerprintVerifier := ssl.NewFingerprintVerifier()

	// Add some fingerprints
	fingerprintVerifier.AddTrustedFingerprint(testFingerprint)
	fingerprintVerifier.AddTrustedFingerprint(testFingerprint2)

	trustedFingerprints := fingerprintVerifier.GetTrustedFingerprints()
	if len(trustedFingerprints) == 0 {
		t.Fatal("No fingerprints in cache")
	}

	// Clear cache
	fingerprintVerifier.ClearCache()

	trustedFingerprints = fingerprintVerifier.GetTrustedFingerprints()
	if len(trustedFingerprints) != 0 {
		t.Errorf("ClearCache() failed to clear cache, still has %d entries", len(trustedFingerprints))
	}
}

func TestNormalizeFingerprint(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		input    string
		expected string
	}{
		{
			name:     "already normalized",
			input:    testFingerprint,
			expected: testFingerprint,
		},
		{
			name:     "lowercase",
			input:    "aa:bb:cc:dd:ee:ff:00:11:22:33:44:55:66:77:88:99:aa:bb:cc:dd:ee:ff:00:11:22:33:44:55:66:77:88:99",
			expected: testFingerprint,
		},
		{
			name:     "no colons",
			input:    "AABBCCDDEEFF00112233445566778899AABBCCDDEEFF00112233445566778899",
			expected: testFingerprint,
		},
		{
			name:     "with spaces",
			input:    "AA BB CC DD EE FF 00 11 22 33 44 55 66 77 88 99 AA BB CC DD EE FF 00 11 22 33 44 55 66 77 88 99",
			expected: testFingerprint,
		},
		{
			name:     "with dashes",
			input:    "AA-BB-CC-DD-EE-FF-00-11-22-33-44-55-66-77-88-99-AA-BB-CC-DD-EE-FF-00-11-22-33-44-55-66-77-88-99",
			expected: testFingerprint,
		},
	}

	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()

			result := ssl.NormalizeFingerprint(testCase.input)
			if result != testCase.expected {
				t.Errorf("ssl.NormalizeFingerprint() = %v, want %v", result, testCase.expected)
			}
		})
	}
}

func TestCompareFingerprints(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		fp1      string
		fp2      string
		expected bool
	}{
		{
			name:     "identical",
			fp1:      testFingerprint,
			fp2:      testFingerprint,
			expected: true,
		},
		{
			name:     "different format same value",
			fp1:      testFingerprint,
			fp2:      "aabbccddeeff00112233445566778899aabbccddeeff00112233445566778899",
			expected: true,
		},
		{
			name:     "different values",
			fp1:      testFingerprint,
			fp2:      testFingerprint2,
			expected: false,
		},
	}

	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()

			result := ssl.CompareFingerprints(testCase.fp1, testCase.fp2)
			if result != testCase.expected {
				t.Errorf("CompareFingerprints() = %v, want %v", result, testCase.expected)
			}
		})
	}
}

func TestFormatFingerprint(t *testing.T) {
	t.Parallel()
	// Test with 32 bytes (SHA256)
	input := []byte{
		0xAA, 0xBB, 0xCC, 0xDD, 0xEE, 0xFF, 0x00, 0x11,
		0x22, 0x33, 0x44, 0x55, 0x66, 0x77, 0x88, 0x99,
		0xAA, 0xBB, 0xCC, 0xDD, 0xEE, 0xFF, 0x00, 0x11,
		0x22, 0x33, 0x44, 0x55, 0x66, 0x77, 0x88, 0x99,
	}

	expected := testFingerprint
	result := ssl.FormatFingerprint(input)

	if result != expected {
		t.Errorf("FormatFingerprint() = %v, want %v", result, expected)
	}
}

func TestParseFingerprint(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		input   string
		wantErr bool
	}{
		{
			name:    "valid fingerprint with colons",
			input:   testFingerprint,
			wantErr: false,
		},
		{
			name:    "valid fingerprint without colons",
			input:   "AABBCCDDEEFF00112233445566778899AABBCCDDEEFF00112233445566778899",
			wantErr: false,
		},
		{
			name:    "invalid hex",
			input:   "ZZ:BB:CC:DD:EE:FF:00:11:22:33:44:55:66:77:88:99:AA:BB:CC:DD:EE:FF:00:11:22:33:44:55:66:77:88:99",
			wantErr: true,
		},
		{
			name:    "wrong length",
			input:   "AA:BB:CC:DD",
			wantErr: true,
		},
	}

	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()

			result, err := ssl.ParseFingerprint(testCase.input)
			if testCase.wantErr {
				if err == nil {
					t.Errorf("ParseFingerprint() expected error, got nil")
				}
			} else {
				if err != nil {
					t.Errorf("ParseFingerprint() unexpected error = %v", err)
				}

				if len(result) != 32 {
					t.Errorf("ParseFingerprint() returned %d bytes, want 32", len(result))
				}
			}
		})
	}
}
