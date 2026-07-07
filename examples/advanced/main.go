package main

import (
	"crypto/x509"
	"errors"
	"log"
	"os"

	"github.com/fivetwenty-io/proxmox-apiclient-go/v3/internal/constants"
	"github.com/fivetwenty-io/proxmox-apiclient-go/v3/internal/ssl"
	pve "github.com/fivetwenty-io/proxmox-apiclient-go/v3/pkg/client"
	pveerrors "github.com/fivetwenty-io/proxmox-apiclient-go/v3/pkg/errors"
)

const exampleFingerprint = "AA:BB:CC:DD:EE:FF:00:11:22:33:44:55:66:77:88:99:AA:BB:CC:DD:EE:FF:00:11:22:33:44:55:66:77:88:99"

func main() {
	log.Println("PVE API Client - Advanced Examples")
	log.Println("==================================")

	host := os.Getenv("PVE_HOST")
	if host == "" {
		host = "localhost"
	}

	// Example 1: SSL/TLS Configuration
	log.Println("\n1. SSL/TLS Configuration:")
	demonstrateSSLConfiguration()

	// Example 2: Error Handling
	log.Println("\n2. Error Handling:")
	demonstrateErrorHandling()

	// Example 3: Client with Custom Options
	log.Println("\n3. Custom Client Configuration:")
	demonstrateCustomClient(host)

	// Example 4: Fingerprint Management
	log.Println("\n4. Certificate Fingerprint Management:")
	demonstrateFingerprintManagement()

	log.Println("\n✅ Advanced examples completed!")
}

func demonstrateSSLConfiguration() {
	// SSL verification modes
	modes := []struct {
		name string
		mode pve.SSLVerifyMode
	}{
		{"No verification (insecure)", pve.SSLVerifyNone},
		{"Verify peer certificate", pve.SSLVerifyPeer},
		{"Verify hostname", pve.SSLVerifyHost},
		{"Full verification", pve.SSLVerifyFull},
	}

	for _, m := range modes {
		log.Printf("  • %s (mode: %d)\n", m.name, m.mode)
	}

	// Example SSL options
	sslOpts := &pve.SSLOptions{
		VerifyHostname: true,
		VerifyMode:     pve.SSLVerifyPeer,
		CACert:         "/path/to/ca.crt",
		ClientCert:     "/path/to/client.crt",
		ClientKey:      "/path/to/client.key",
	}

	log.Println("\n  Example SSL configuration:")
	log.Printf("    - Verify Hostname: %v\n", sslOpts.VerifyHostname)
	log.Printf("    - Verify Mode: %v\n", sslOpts.VerifyMode)
	log.Printf("    - CA Certificate: %s\n", sslOpts.CACert)
	log.Printf("    - Client Certificate: %s\n", sslOpts.ClientCert)
	log.Printf("    - Client Key: %s\n", sslOpts.ClientKey)
}

func demonstrateErrorHandling() {
	testErrors := createTestErrors()
	demonstrateDifferentErrorTypes(testErrors)
	demonstrateErrorCodeAnalysis()
}

func createTestErrors() []error {
	apiErr := &pveerrors.APIError{
		Message:  "API operation failed",
		Code:     constants.HTTPStatusInternalServerError,
		HTTPCode: constants.HTTPStatusInternalServerError,
	}

	authErr := &pveerrors.AuthenticationError{
		APIError: pveerrors.APIError{
			Message:  "Invalid credentials",
			Code:     constants.HTTPStatusUnauthorized,
			HTTPCode: constants.HTTPStatusUnauthorized,
		},
		Realm: "pam",
	}

	tfaErr := &pveerrors.TFARequiredError{
		Ticket: "partial-ticket",
		Types:  []string{"totp", "recovery"},
	}

	permErr := &pveerrors.PermissionError{
		APIError: pveerrors.APIError{
			Message:  "Access denied",
			Code:     constants.HTTPStatusForbidden,
			HTTPCode: constants.HTTPStatusForbidden,
		},
		What: "/vms/100",
	}

	connErr := &pveerrors.ConnectionError{
		Host:    "pve.example.com",
		Port:    constants.ProxmoxDefaultPort,
		Message: "Connection refused",
	}

	timeoutErr := &pveerrors.TimeoutError{
		Operation: "login",
		Duration:  "30s",
	}

	return []error{apiErr, authErr, tfaErr, permErr, connErr, timeoutErr}
}

func demonstrateDifferentErrorTypes(testErrors []error) {
	for _, err := range testErrors {
		log.Printf("\n  Error: %v\n", err)
		checkErrorTypes(err)
	}
}

func demonstrateErrorCodeAnalysis() {
	log.Println("\n  Error Code Analysis:")

	codes := []int{200, 401, 403, 404, 429, 500, 503}
	for _, code := range codes {
		var status string

		switch {
		case pveerrors.IsSuccessCode(code):
			status = "Success"
		case pveerrors.IsClientErrorCode(code):
			status = "Client Error"
		case pveerrors.IsServerErrorCode(code):
			status = "Server Error"
		}

		log.Printf("    Code %d: %s", code, status)

		if pveerrors.IsRetryableCode(code) {
			log.Printf(" (Retryable) - %s\n", pveerrors.GetErrorMessage(code))
		} else {
			log.Printf(" - %s\n", pveerrors.GetErrorMessage(code))
		}
	}
}

func demonstrateCustomClient(host string) {
	// Create client with all custom options
	opts := pve.Options{
		Host:     host,
		Port:     constants.ProxmoxDefaultPort,
		Protocol: "https",

		// Use environment variables if available
		Username: os.Getenv("PVE_USERNAME"),
		Password: os.Getenv("PVE_PASSWORD"),
		APIToken: os.Getenv("PVE_API_TOKEN"),

		// Timeouts and connection settings
		Timeout:   constants.DefaultTimeout(),
		KeepAlive: constants.DefaultKeepAliveSeconds,

		// SSL configuration
		SSLOptions: &pve.SSLOptions{
			VerifyHostname: false,
			VerifyMode:     pve.SSLVerifyNone, // For testing only!
		},

		// Cached fingerprints
		CachedFingerprints: map[string]bool{
			exampleFingerprint: true,
		},

		// Manual verification callback
		VerifyFingerprintCallback: func(cert *x509.Certificate) bool {
			log.Printf("    🔐 Certificate verification requested\n")
			log.Printf("       Subject: %s\n", cert.Subject)
			log.Printf("       Issuer: %s\n", cert.Issuer)
			// In production, you would properly verify the certificate
			return true
		},

		// Cookie name (usually default is fine)
		CookieName: "PVEAuthCookie",
	}

	log.Println("  Custom client configuration:")
	log.Printf("    - Base URL: %s\n", opts.GetBaseURL())
	log.Printf("    - Timeout: %v\n", opts.Timeout)
	log.Printf("    - Keep-Alive Connections: %d\n", opts.KeepAlive)
	log.Printf("    - Using API Token: %v\n", opts.IsUsingAPIToken())
	log.Printf("    - Using Ticket: %v\n", opts.IsUsingTicket())
	log.Printf("    - Needs Login: %v\n", opts.NeedsLogin())

	// Try to create the client
	if opts.Username != "" || opts.APIToken != "" {
		client, err := pve.NewClient(opts)
		if err != nil {
			log.Printf("    ✗ Failed to create client: %v", err)
		} else {
			log.Println("    ✓ Client created successfully")

			// You can modify client settings after creation
			client.SetTimeout(constants.MediumTimeout())

			const demonstrationKeepAlive = 20
			client.SetKeepAlive(demonstrationKeepAlive) // Keep specific value for demonstration
			log.Println("    ✓ Updated timeout and keep-alive settings")
		}
	} else {
		log.Println("    ⚠ Skipped client creation (no credentials)")
	}
}

func demonstrateFingerprintManagement() {
	// Create a fingerprint verifier
	verifier := ssl.NewFingerprintVerifier()

	// Add trusted fingerprints
	trustedFingerprints := []string{
		exampleFingerprint,
		"11:22:33:44:55:66:77:88:99:AA:BB:CC:DD:EE:FF:00:11:22:33:44:55:66:77:88:99:AA:BB:CC:DD:EE:FF:00",
	}

	verifier.AddTrustedFingerprints(trustedFingerprints)
	log.Printf("  Added %d trusted fingerprints\n", len(trustedFingerprints))

	// Demonstrate fingerprint normalization
	log.Println("\n  Fingerprint normalization examples:")

	variations := []string{
		"aa:bb:cc:dd:ee:ff:00:11:22:33:44:55:66:77:88:99:aa:bb:cc:dd:ee:ff:00:11:22:33:44:55:66:77:88:99",
		"AABBCCDDEEFF00112233445566778899AABBCCDDEEFF00112233445566778899",
		"AA-BB-CC-DD-EE-FF-00-11-22-33-44-55-66-77-88-99-AA-BB-CC-DD-EE-FF-00-11-22-33-44-55-66-77-88-99",
		"AA BB CC DD EE FF 00 11 22 33 44 55 66 77 88 99 AA BB CC DD EE FF 00 11 22 33 44 55 66 77 88 99",
	}

	for _, fp := range variations {
		normalized := ssl.NormalizeFingerprint(fp)
		log.Printf("    Input:  %.30s...\n", fp)
		log.Printf("    Output: %.30s...\n", normalized)
		log.Println()
	}

	// Compare fingerprints
	fp1 := exampleFingerprint
	fp2 := "aabbccddeeff00112233445566778899aabbccddeeff00112233445566778899"

	if ssl.CompareFingerprints(fp1, fp2) {
		log.Println("  ✓ Fingerprints match (different formats, same value)")
	} else {
		log.Println("  ✗ Fingerprints don't match")
	}

	// Get all trusted fingerprints
	trusted := verifier.GetTrustedFingerprints()
	log.Printf("\n  Currently trusted fingerprints: %d\n", len(trusted))

	for i, fp := range trusted {
		log.Printf("    %d. %.50s...\n", i+1, fp)
	}

	// Clear cache
	verifier.ClearCache()
	log.Println("\n  ✓ Fingerprint cache cleared")
}

func checkErrorTypes(err error) {
	if pveerrors.IsAPIError(err) {
		log.Println("    → Is API Error")
		checkAPIErrorDetails(err)
	}

	if pveerrors.IsTFARequired(err) {
		log.Println("    → TFA Required")
	}

	if pveerrors.IsConnectionError(err) {
		log.Println("    → Connection Error")
	}

	if pveerrors.IsTimeoutError(err) {
		log.Println("    → Timeout Error")
	}
}

func checkAPIErrorDetails(err error) {
	var apiErr *pveerrors.APIError
	if !errors.As(err, &apiErr) {
		return
	}

	if apiErr.IsNotFound() {
		log.Println("      • Resource not found")
	}

	if apiErr.IsUnauthorized() {
		log.Println("      • Unauthorized")
	}

	if apiErr.IsForbidden() {
		log.Println("      • Forbidden")
	}
}
