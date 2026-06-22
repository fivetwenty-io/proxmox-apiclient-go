//go:build zap

package main

import (
	"context"
	"os"

	pve "github.com/fivetwenty-io/pve-apiclient-go/v3/pkg/client"
	"github.com/fivetwenty-io/pve-apiclient-go/v3/pkg/logging/zapadapter"
	"go.uber.org/zap"
)

func main() {
	cli, _ := pve.NewClient(pve.Options{
		Host:     "localhost",
		Protocol: "https",
		Port:     8006,
		APIToken: "user@pam!tok=secret",
	})

	// Production zap logger
	zl, _ := zap.NewProduction()
	defer zl.Sync()
	cli.SetLogger(&zapadapter.Adapter{L: zl})

	// Configure log behavior
	cfg := cli.GetLogConfig()
	cfg.LogRequestHeader = true
	cfg.LogResponseHeader = true
	cfg.LogResponseBody = false
	cfg.MaxBodyBytes = 2048
	cli.SetLogConfig(cfg)

	// Per-request fields/toggles
	ctx := context.Background()
	ctx = pve.WithLogFields(ctx, map[string]interface{}{"example": "logging-zap"})
	ctx = pve.WithLogging(ctx, true)

	// _, _ = cli.GetRawCtx(ctx, "/version", nil)
	_ = os.Getenv // keep import
}
