// Package network is a hand-written convenience layer over the PVE
// /nodes/{node}/network endpoints, offering idempotent bridge helpers
// (EnsureBridge, DeleteBridge, BridgeExists) and a Reload operation. It
// trades the generated package's full parameter and response typing for a
// smaller, opinionated surface built around map[string]interface{}.
//
// The complete typed API for node network configuration — every parameter,
// every response field, every endpoint under /nodes/{node}/network — lives
// in the generated package
// github.com/fivetwenty-io/pve-apiclient-go/v3/pkg/api/nodes. Prefer this
// package when you want idempotent bridge create/delete without hand-rolling
// the existence check; reach for pkg/api/nodes when you need a parameter or
// response field this package does not expose, or when you want
// compile-time-checked request/response types.
package network

import (
	"context"
	"fmt"
	"net/url"

	"github.com/fivetwenty-io/pve-apiclient-go/v3/pkg/client"
)

// Service defines network helpers.
type Service interface {
	EnsureBridge(ctx context.Context, node, bridge string, params map[string]interface{}) error
	DeleteBridge(ctx context.Context, node, bridge string) error
	BridgeExists(ctx context.Context, node, bridge string) (bool, error)
	Reload(ctx context.Context, node string) error
}

type service struct{ c client.Client }

// New returns a new network service.
//
//nolint:ireturn // Factory pattern - returns interface to encapsulate implementation and enable mocking
func New(c client.Client) Service { return &service{c: c} }

func (s *service) EnsureBridge(ctx context.Context, node, bridge string, params map[string]interface{}) error {
	exists, err := s.BridgeExists(ctx, node, bridge)
	if err != nil {
		return err
	}

	if exists {
		return nil
	}

	if params == nil {
		params = map[string]interface{}{}
	}

	if _, ok := params["type"]; !ok {
		params["type"] = "bridge"
	}

	if _, ok := params["iface"]; !ok {
		params["iface"] = bridge
	}

	_, err = s.c.PostCtx(ctx, fmt.Sprintf("/nodes/%s/network", url.PathEscape(node)), params)
	if err != nil {
		return fmt.Errorf("failed to create bridge: %w", err)
	}

	return nil
}

func (s *service) DeleteBridge(ctx context.Context, node, bridge string) error {
	exists, err := s.BridgeExists(ctx, node, bridge)
	if err != nil {
		return err
	}

	if !exists {
		return nil
	}

	_, err = s.c.DeleteCtx(ctx, fmt.Sprintf("/nodes/%s/network/%s", url.PathEscape(node), url.PathEscape(bridge)), nil)
	if err != nil {
		return fmt.Errorf("failed to delete bridge: %w", err)
	}

	return nil
}

func (s *service) BridgeExists(ctx context.Context, node, bridge string) (bool, error) {
	data, err := s.c.GetCtx(ctx, fmt.Sprintf("/nodes/%s/network", url.PathEscape(node)), nil)
	if err != nil {
		return false, fmt.Errorf("failed to get network interfaces for node %q: %w", node, err)
	}
	// Expect a list of maps
	if list, ok := data.([]interface{}); ok {
		for _, it := range list {
			if m, ok := it.(map[string]interface{}); ok {
				if iface, _ := m["iface"].(string); iface == bridge {
					return true, nil
				}
			}
		}
	}

	return false, nil
}

func (s *service) Reload(ctx context.Context, node string) error {
	_, err := s.c.PostCtx(ctx, fmt.Sprintf("/nodes/%s/network", url.PathEscape(node)), map[string]interface{}{"reload": 1})
	if err != nil {
		return fmt.Errorf("failed to reload network configuration for node %q: %w", node, err)
	}

	return nil
}
