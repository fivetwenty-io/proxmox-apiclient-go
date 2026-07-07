//go:build zap

package zapadapter

import (
	ih "github.com/fivetwenty-io/proxmox-apiclient-go/v3/internal/http"
	"go.uber.org/zap"
)

// Adapter wraps a zap.Logger to satisfy internal/http.Logger.
type Adapter struct{ L *zap.Logger }

func (a *Adapter) with(fields map[string]interface{}) *zap.Logger {
	if a == nil || a.L == nil || len(fields) == 0 {
		return a.L
	}
	fs := make([]zap.Field, 0, len(fields))
	for k, v := range fields {
		fs = append(fs, zap.Any(k, v))
	}
	return a.L.With(fs...)
}

func (a *Adapter) Debug(msg string, fields map[string]interface{}) { a.with(fields).Debug(msg) }
func (a *Adapter) Info(msg string, fields map[string]interface{})  { a.with(fields).Info(msg) }
func (a *Adapter) Warn(msg string, fields map[string]interface{})  { a.with(fields).Warn(msg) }
func (a *Adapter) Error(msg string, fields map[string]interface{}) { a.with(fields).Error(msg) }

// Set installs this adapter on an internal HTTP client.
func Set(c *ih.Client, l *zap.Logger) { c.SetLogger(&Adapter{L: l}) }
