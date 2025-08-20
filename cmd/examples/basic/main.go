package main

import (
	"fmt"
	"log"
	"os"

	pve "github.com/fivetwenty-io/pve-apicilent-go/pkg/client"
)

func main() {
	// Get configuration from environment variables
	host := os.Getenv("PVE_HOST")
	if host == "" {
		host = "localhost"
	}

	username := os.Getenv("PVE_USERNAME")
	if username == "" {
		username = "root@pam"
	}

	password := os.Getenv("PVE_PASSWORD")
	apiToken := os.Getenv("PVE_API_TOKEN")

	// Create client options
	opts := pve.Options{
		Host:     host,
		Protocol: "https",
		Port:     8006,
	}

	// Configure authentication
	if apiToken != "" {
		opts.APIToken = apiToken
		fmt.Println("Using API token authentication")
	} else if password != "" {
		opts.Username = username
		opts.Password = password
		fmt.Printf("Using username/password authentication for %s\n", username)
	} else {
		log.Fatal("Either PVE_PASSWORD or PVE_API_TOKEN must be set")
	}

	// Create the client
	client, err := pve.NewClient(opts)
	if err != nil {
		log.Fatalf("Failed to create client: %v", err)
	}

	fmt.Printf("Successfully created PVE client for %s\n", host)

	// Test a simple API call
	fmt.Println("\nTesting API call to /version:")
	version, err := client.Get("/version", nil)
	if err != nil {
		log.Fatalf("Failed to get version: %v", err)
	}

	fmt.Printf("Version response: %v\n", version)

	// Test cluster status
	fmt.Println("\nTesting API call to /cluster/status:")
	status, err := client.Get("/cluster/status", nil)
	if err != nil {
		// This might fail if not in a cluster
		fmt.Printf("Cluster status failed (expected if not clustered): %v\n", err)
	} else {
		fmt.Printf("Cluster status: %v\n", status)
	}

	// Test nodes listing
	fmt.Println("\nTesting API call to /nodes:")
	nodes, err := client.Get("/nodes", nil)
	if err != nil {
		log.Fatalf("Failed to get nodes: %v", err)
	}

	fmt.Printf("Nodes: %v\n", nodes)

	// Logout if using ticket authentication
	if apiToken == "" {
		fmt.Println("\nLogging out...")
		if err := client.Logout(); err != nil {
			fmt.Printf("Logout failed: %v\n", err)
		} else {
			fmt.Println("Logged out successfully")
		}
	}

	fmt.Println("\nExample completed successfully!")
}
