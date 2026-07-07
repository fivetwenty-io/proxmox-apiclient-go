package main

import (
	"log"
	"log/slog"
	"os"

	"github.com/fivetwenty-io/proxmox-apiclient-go/v3/internal/constants"
	pve "github.com/fivetwenty-io/proxmox-apiclient-go/v3/pkg/client"
)

func main() {
	// Setup structured logging
	logger := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo}))
	slog.SetDefault(logger)

	opts := getClientOptions()
	client := createClient(opts)
	runAPITests(client)
	logoutIfNeeded(client, opts.APIToken)
	slog.Info("Example completed successfully")
}

func getClientOptions() pve.Options {
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

	opts := pve.Options{
		Host:     host,
		Protocol: "https",
		Port:     constants.ProxmoxDefaultPort,
	}

	switch {
	case apiToken != "":
		opts.APIToken = apiToken

		slog.Info("Using API token authentication")
	case password != "":
		opts.Username = username
		opts.Password = password

		slog.Info("Using username/password authentication", "username", username)
	default:
		log.Fatal("Either PVE_PASSWORD or PVE_API_TOKEN must be set")
	}

	return opts
}

func createClient(opts pve.Options) pve.Client { //nolint:ireturn // Helper function for example
	client, err := pve.NewClient(opts)
	if err != nil {
		log.Fatalf("Failed to create client: %v", err)
	}

	slog.Info("Successfully created PVE client", "host", opts.Host)

	return client
}

func runAPITests(client pve.Client) {
	testVersion(client)
	testClusterStatus(client)
	testNodes(client)
}

func testVersion(client pve.Client) {
	slog.Info("Testing API call", "endpoint", "/version")

	version, err := client.Get("/version", nil)
	if err != nil {
		log.Fatalf("Failed to get version: %v", err)
	}

	slog.Info("Version response received", "data", version)
}

func testClusterStatus(client pve.Client) {
	slog.Info("Testing API call", "endpoint", "/cluster/status")

	status, err := client.Get("/cluster/status", nil)
	if err != nil {
		slog.Warn("Cluster status failed (expected if not clustered)", "error", err)
	} else {
		slog.Info("Cluster status received", "data", status)
	}
}

func testNodes(client pve.Client) {
	slog.Info("Testing API call", "endpoint", "/nodes")

	nodes, err := client.Get("/nodes", nil)
	if err != nil {
		log.Fatalf("Failed to get nodes: %v", err)
	}

	slog.Info("Nodes response received", "data", nodes)
}

func logoutIfNeeded(client pve.Client, apiToken string) {
	if apiToken == "" {
		slog.Info("Logging out")

		err := client.Logout()
		if err != nil {
			slog.Error("Logout failed", "error", err)
		} else {
			slog.Info("Logged out successfully")
		}
	}
}
