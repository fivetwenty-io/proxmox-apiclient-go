package main

import (
	"log"
	"os"

	"github.com/fivetwenty-io/proxmox-apiclient-go/v3/pkg/auth"
	pve "github.com/fivetwenty-io/proxmox-apiclient-go/v3/pkg/client"
)

func main() {
	log.Println("PVE API Client - Authentication Examples")
	log.Println("========================================")

	host := os.Getenv("PVE_HOST")
	if host == "" {
		host = "localhost"
	}

	// Example 1: Username/Password Authentication
	log.Println("\n1. Username/Password Authentication:")
	testPasswordAuth(host)

	// Example 2: API Token Authentication
	log.Println("\n2. API Token Authentication:")
	testAPITokenAuth(host)

	// Example 3: Parse and validate API token
	log.Println("\n3. API Token Parsing:")
	testTokenParsing()

	// Example 4: Validate token ID format
	log.Println("\n4. Token ID Validation:")
	demonstrateTokenIDValidation()

	// Example 5: Interactive credentials (demonstration only)
	log.Println("\n5. Interactive Authentication (Demo):")
	demonstrateInteractiveAuth()

	// Example 6: TFA Types
	log.Println("\n6. Two-Factor Authentication Types:")
	demonstrateTFATypes()

	log.Println("\n✅ Authentication examples completed!")
}

func testPasswordAuth(host string) {
	password := os.Getenv("PVE_PASSWORD")
	if password == "" {
		log.Println("  ⚠ Skipped: Set PVE_PASSWORD to test")

		return
	}

	username := os.Getenv("PVE_USERNAME")
	if username == "" {
		username = "root@pam"
	}

	opts := pve.Options{
		Host:     host,
		Username: username,
		Password: password,
	}

	client, err := pve.NewClient(opts)
	if err != nil {
		log.Printf("Failed to create client with username/password: %v", err)

		return
	}

	log.Printf("  ✓ Successfully authenticated as %s\n", username)

	version, err := client.Get("/version", nil)
	if err == nil {
		log.Printf("  ✓ API Version: %v\n", version)
	}

	err = client.Logout()
	if err == nil {
		log.Println("  ✓ Successfully logged out")
	}
}

func testAPITokenAuth(host string) {
	apiToken := os.Getenv("PVE_API_TOKEN")
	if apiToken == "" {
		log.Println("  ⚠ Skipped: Set PVE_API_TOKEN to test")

		return
	}

	opts := pve.Options{
		Host:     host,
		APIToken: apiToken,
	}

	client, err := pve.NewClient(opts)
	if err != nil {
		log.Printf("Failed to create client with API token: %v", err)

		return
	}

	log.Println("  ✓ Successfully authenticated with API token")

	version, err := client.Get("/version", nil)
	if err == nil {
		log.Printf("  ✓ API Version: %v\n", version)
	}

	log.Println("  ℹ API tokens dont require logout")
}

func testTokenParsing() {
	exampleToken := "root@pam!mytoken=12345678-90ab-cdef-1234-567890abcdef"

	token, err := auth.ParseAPIToken(exampleToken)
	if err != nil {
		log.Printf("Failed to parse token: %v", err)

		return
	}

	log.Printf("  ✓ Parsed token ID: %s\n", token.ID)
	log.Printf("  ✓ Token secret length: %d chars\n", len(token.Secret))
}

func demonstrateTokenIDValidation() {
	validIDs := []string{
		"root@pam!mytoken",
		"user@pve!api-access",
		"automation@ldap!ci-token",
	}
	invalidIDs := []string{
		"root",         // Missing realm and token name
		"root@pam",     // Missing token name
		"root!mytoken", // Missing realm
	}

	log.Println("  Valid token IDs:")

	for _, id := range validIDs {
		err := auth.ValidateTokenID(id)
		if err == nil {
			log.Printf("    ✓ %s\n", id)
		}
	}

	log.Println("  Invalid token IDs:")

	for _, id := range invalidIDs {
		err := auth.ValidateTokenID(id)
		if err != nil {
			log.Printf("    ✗ %s: %v\n", id, err)
		}
	}
}

func demonstrateInteractiveAuth() {
	log.Println("  ℹ In a real scenario, you would use:")
	log.Println("    - auth.PromptUsername() to get username")
	log.Println("    - auth.PromptPassword() to get password securely")
	log.Println("    - auth.PromptCredentials() to get both")
}

func demonstrateTFATypes() {
	tfaTypes := []string{
		string(auth.TFATypeTOTP),
		string(auth.TFATypeYubico),
		string(auth.TFATypeRecovery),
		string(auth.TFATypeU2F),
		string(auth.TFATypeWebAuthn),
	}
	for _, tfaType := range tfaTypes {
		log.Printf("  • %s\n", tfaType)
	}
}
