package main

import (
	"log"
	"net/http"
	"os"
	"time"

	"github.com/fivetwenty-io/proxmox-apiclient-go/v3/internal/constants"
	client "github.com/fivetwenty-io/proxmox-apiclient-go/v3/pkg/client"
	pmetrics "github.com/fivetwenty-io/proxmox-apiclient-go/v3/pkg/metrics"
)

func main() {
	host := getenv("PVE_HOST", "127.0.0.1")
	user := os.Getenv("PVE_USER")
	pass := os.Getenv("PVE_PASS")
	token := os.Getenv("PVE_TOKEN")

	const exampleKeepAlive = 4

	pveClient, err := client.NewClient(client.Options{
		Host:      host,
		Protocol:  "https",
		Username:  user,
		Password:  pass,
		APIToken:  token,
		Timeout:   constants.ShortTimeout(),
		KeepAlive: exampleKeepAlive, // Example specific value
	})
	if err != nil {
		log.Fatalf("new client: %v", err)
	}

	// Attach Prometheus-friendly metrics collector
	metrics := pmetrics.NewDefaultMetrics()
	pveClient.SetMetrics(metrics)

	// Kick one call to produce a sample metric
	_, _ = pveClient.GetRaw("/version", nil)

	// Serve metrics in Prometheus text format
	http.HandleFunc("/metrics", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain; version=0.0.4")

		err := metrics.Export(w)
		if err != nil {
			http.Error(w, err.Error(), constants.HTTPStatusInternalServerError)
		}
	})

	addr := ":2112"
	log.Printf("serving metrics at http://%s/metrics", addr)

	server := &http.Server{
		Addr:         addr,
		Handler:      nil,
		ReadTimeout:  constants.DefaultKeepAliveSeconds * time.Second,
		WriteTimeout: constants.DefaultKeepAliveSeconds * time.Second,
		IdleTimeout:  constants.MediumTimeout(),
	}
	log.Fatal(server.ListenAndServe())
}

func getenv(k, d string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}

	return d
}
