package slogadapter

import (
	"log/slog"

	"github.com/fivetwenty-io/proxmox-apiclient-go/v3/internal/constants"
	ih "github.com/fivetwenty-io/proxmox-apiclient-go/v3/internal/http"
)

// Adapter wraps a slog.Logger to satisfy internal/http.Logger.
type Adapter struct{ L *slog.Logger }

// New creates a new slog adapter.
func New(l *slog.Logger) *Adapter { return &Adapter{L: l} }

func (a *Adapter) Debug(msg string, fields map[string]interface{}) { a.with(fields).Debug(msg) }
func (a *Adapter) Info(msg string, fields map[string]interface{})  { a.with(fields).Info(msg) }
func (a *Adapter) Warn(msg string, fields map[string]interface{})  { a.with(fields).Warn(msg) }
func (a *Adapter) Error(msg string, fields map[string]interface{}) { a.with(fields).Error(msg) }

func (a *Adapter) with(fields map[string]interface{}) *slog.Logger {
	if a == nil || a.L == nil || len(fields) == 0 {
		return a.L
	}

	attrs := make([]any, 0, len(fields)*constants.AttributeMultiplier)
	for k, v := range fields {
		attrs = append(attrs, k, v)
	}

	return a.L.With(attrs...)
}

// Set installs this adapter on an internal HTTP client.
func Set(c *ih.Client, l *slog.Logger) { c.SetLogger(&Adapter{L: l}) }
