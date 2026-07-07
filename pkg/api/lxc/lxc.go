// Package lxc is a hand-written convenience layer over a focused subset of
// the PVE /nodes/{node}/lxc endpoints (list, create, status, lifecycle,
// config, clone, resize). It trades the generated packages' full parameter
// and response typing for a smaller, opinionated surface built around
// map[string]interface{} and a handful of purpose-built structs.
//
// The complete typed API for LXC containers — every parameter, every
// response field, every endpoint under /nodes/{node}/lxc — lives in the
// generated package github.com/fivetwenty-io/proxmox-apiclient-go/v3/pkg/api/nodes.
// Prefer this package for common lifecycle operations (create, start, stop,
// clone, resize) where the narrower surface reads more directly; reach for
// pkg/api/nodes when you need a parameter or response field this package does
// not expose, or when you want compile-time-checked request/response types.
package lxc

import (
	"context"
	"errors"
	"fmt"
	"net/url"

	"github.com/fivetwenty-io/proxmox-apiclient-go/v3/pkg/client"
)

// ErrUnexpectedResponseType is returned when the API response type is not what was expected.
var ErrUnexpectedResponseType = errors.New("unexpected response type")

// Service defines LXC container management operations for a single node.
// Unlike the Service interfaces in the sibling qemu/network/storage/tasks/
// cloudinit packages, the node is bound at construction time (via NewService
// or NewClient) rather than passed per call — this mirrors Client's existing
// method set, which was designed around a single node per instance.
type Service interface {
	List(ctx context.Context) ([]ContainerStatus, error)
	Create(ctx context.Context, config ContainerConfig) (string, error)
	Status(ctx context.Context, vmid int) (*ContainerStatus, error)
	Start(ctx context.Context, vmid int) (string, error)
	Stop(ctx context.Context, vmid int) (string, error)
	Shutdown(ctx context.Context, vmid int, timeout int) (string, error)
	Reboot(ctx context.Context, vmid int) (string, error)
	Delete(ctx context.Context, vmid int, purge bool) (string, error)
	GetConfig(ctx context.Context, vmid int) (map[string]interface{}, error)
	UpdateConfig(ctx context.Context, vmid int, config map[string]interface{}) error
	Clone(ctx context.Context, vmid int, newID int, opts CloneOptions) (string, error)
	Resize(ctx context.Context, vmid int, disk string, size string) error
}

// Client provides LXC container management operations.
type Client struct {
	pveClient client.Client
	node      string
}

// NewClient creates a new LXC API client for the specified node.
func NewClient(pveClient client.Client, node string) *Client {
	return &Client{
		pveClient: pveClient,
		node:      node,
	}
}

// NewService returns a Service bound to the given node, mirroring the
// New(c client.Client) Service factory used by the sibling qemu/network/
// storage/tasks/cloudinit packages. Since LXC operations are inherently
// per-node (list, create, and lifecycle calls all target one node's
// /nodes/{node}/lxc endpoints), the node is supplied here rather than on
// every call. The returned Service is the same concrete type as NewClient
// returns, so existing callers of NewClient are unaffected.
//
//nolint:ireturn // Factory pattern - returns interface to encapsulate implementation and enable mocking
func NewService(pveClient client.Client, node string) Service {
	return NewClient(pveClient, node)
}

// ContainerConfig represents LXC container configuration.
type ContainerConfig struct {
	VMID         int               `json:"vmid"`
	OSTemplate   string            `json:"ostemplate"`
	Hostname     string            `json:"hostname,omitempty"`
	Description  string            `json:"description,omitempty"`
	Memory       int               `json:"memory,omitempty"`       // MB
	Swap         int               `json:"swap,omitempty"`         // MB
	Cores        int               `json:"cores,omitempty"`        // CPU cores
	CPULimit     int               `json:"cpulimit,omitempty"`     // CPU limit (0-128)
	CPUUnits     int               `json:"cpuunits,omitempty"`     // CPU weight
	RootFS       string            `json:"rootfs,omitempty"`       // Root filesystem (e.g., "local:8")
	Net0         string            `json:"net0,omitempty"`         // Network config
	Unprivileged bool              `json:"unprivileged,omitempty"` // Unprivileged container
	Features     map[string]string `json:"features,omitempty"`     // Features (nesting, fuse, etc.)
	Password     string            `json:"password,omitempty"`     // Root password
	SSHKeys      string            `json:"ssh-public-keys,omitempty"`
	Nameserver   string            `json:"nameserver,omitempty"`
	Searchdomain string            `json:"searchdomain,omitempty"`
	Start        bool              `json:"start,omitempty"` // Start after creation
	Storage      string            `json:"storage,omitempty"`
	Pool         string            `json:"pool,omitempty"` // Resource pool
}

// ContainerStatus represents the current status of an LXC container.
type ContainerStatus struct {
	VMID    int    `json:"vmid"`
	Status  string `json:"status"` // running, stopped, etc.
	Name    string `json:"name"`
	Uptime  int64  `json:"uptime,omitempty"`
	CPUs    int    `json:"cpus,omitempty"`
	MaxMem  int64  `json:"maxmem,omitempty"`
	Mem     int64  `json:"mem,omitempty"`
	MaxDisk int64  `json:"maxdisk,omitempty"`
	Disk    int64  `json:"disk,omitempty"`
}

// List returns all LXC containers on the node.
func (c *Client) List(ctx context.Context) ([]ContainerStatus, error) {
	path := fmt.Sprintf("/nodes/%s/lxc", url.PathEscape(c.node))

	resp, err := c.pveClient.GetCtx(ctx, path, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to list LXC containers: %w", err)
	}

	return parseContainerList(resp), nil
}

// parseContainerList extracts container status information from API response.
func parseContainerList(resp interface{}) []ContainerStatus {
	data, ok := resp.([]interface{})
	if !ok {
		return []ContainerStatus{}
	}

	containers := make([]ContainerStatus, 0, len(data))
	for _, item := range data {
		if status, ok := parseContainerItem(item); ok {
			containers = append(containers, status)
		}
	}

	return containers
}

// parseContainerItem converts a single API response item to ContainerStatus.
func parseContainerItem(item interface{}) (ContainerStatus, bool) {
	containerData, ok := item.(map[string]interface{})
	if !ok {
		return ContainerStatus{}, false
	}

	status := ContainerStatus{
		Status: getString(containerData, "status"),
		Name:   getString(containerData, "name"),
	}

	if vmid, ok := containerData["vmid"].(float64); ok {
		status.VMID = int(vmid)
	}

	if uptime, ok := containerData["uptime"].(float64); ok {
		status.Uptime = int64(uptime)
	}

	return status, true
}

// Create creates a new LXC container.
func (c *Client) Create(ctx context.Context, config ContainerConfig) (string, error) {
	path := fmt.Sprintf("/nodes/%s/lxc", url.PathEscape(c.node))
	params := buildCreateParams(config)

	resp, err := c.pveClient.PostCtx(ctx, path, params)
	if err != nil {
		return "", fmt.Errorf("failed to create LXC container: %w", err)
	}

	return extractUPID(resp)
}

// buildCreateParams constructs the parameters map for container creation.
func buildCreateParams(config ContainerConfig) map[string]interface{} {
	params := map[string]interface{}{
		"vmid":       config.VMID,
		"ostemplate": config.OSTemplate,
	}

	addStringParam(params, "hostname", config.Hostname)
	addStringParam(params, "description", config.Description)
	addStringParam(params, "rootfs", config.RootFS)
	addStringParam(params, "net0", config.Net0)
	addStringParam(params, "password", config.Password)
	addStringParam(params, "ssh-public-keys", config.SSHKeys)
	addStringParam(params, "nameserver", config.Nameserver)
	addStringParam(params, "searchdomain", config.Searchdomain)
	addStringParam(params, "storage", config.Storage)
	addStringParam(params, "pool", config.Pool)

	addIntParam(params, "memory", config.Memory)
	addIntParam(params, "swap", config.Swap)
	addIntParam(params, "cores", config.Cores)
	addIntParam(params, "cpulimit", config.CPULimit)
	addIntParam(params, "cpuunits", config.CPUUnits)

	addBoolParam(params, "unprivileged", config.Unprivileged)
	addBoolParam(params, "start", config.Start)

	// Add features
	for key, value := range config.Features {
		params[key] = value
	}

	return params
}

// addStringParam adds a non-empty string parameter to the params map.
func addStringParam(params map[string]interface{}, key, value string) {
	if value != "" {
		params[key] = value
	}
}

// addIntParam adds a non-zero integer parameter to the params map.
func addIntParam(params map[string]interface{}, key string, value int) {
	if value > 0 {
		params[key] = value
	}
}

// addBoolParam adds a boolean parameter as 1 if true.
func addBoolParam(params map[string]interface{}, key string, value bool) {
	if value {
		params[key] = 1
	}
}

// extractUPID extracts the UPID string from the API response.
func extractUPID(resp interface{}) (string, error) {
	if upid, ok := resp.(string); ok {
		return upid, nil
	}

	return "", fmt.Errorf("%w: %T", ErrUnexpectedResponseType, resp)
}

// Status returns the current status of an LXC container.
func (c *Client) Status(ctx context.Context, vmid int) (*ContainerStatus, error) {
	path := fmt.Sprintf("/nodes/%s/lxc/%d/status/current", url.PathEscape(c.node), vmid)

	resp, err := c.pveClient.GetCtx(ctx, path, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to get container status: %w", err)
	}

	return parseContainerStatus(resp, vmid), nil
}

// parseContainerStatus extracts detailed container status from API response.
func parseContainerStatus(resp interface{}, vmid int) *ContainerStatus {
	data, ok := resp.(map[string]interface{})
	if !ok {
		return &ContainerStatus{VMID: vmid}
	}

	status := &ContainerStatus{
		VMID:   vmid,
		Status: getString(data, "status"),
		Name:   getString(data, "name"),
	}

	populateContainerMetrics(status, data)

	return status
}

// populateContainerMetrics fills in numeric metrics from the API response data.
func populateContainerMetrics(status *ContainerStatus, data map[string]interface{}) {
	if uptime, ok := data["uptime"].(float64); ok {
		status.Uptime = int64(uptime)
	}

	if cpus, ok := data["cpus"].(float64); ok {
		status.CPUs = int(cpus)
	}

	if maxmem, ok := data["maxmem"].(float64); ok {
		status.MaxMem = int64(maxmem)
	}

	if mem, ok := data["mem"].(float64); ok {
		status.Mem = int64(mem)
	}

	if maxdisk, ok := data["maxdisk"].(float64); ok {
		status.MaxDisk = int64(maxdisk)
	}

	if disk, ok := data["disk"].(float64); ok {
		status.Disk = int64(disk)
	}
}

// Start starts an LXC container.
func (c *Client) Start(ctx context.Context, vmid int) (string, error) {
	path := fmt.Sprintf("/nodes/%s/lxc/%d/status/start", url.PathEscape(c.node), vmid)

	resp, err := c.pveClient.PostCtx(ctx, path, nil)
	if err != nil {
		return "", fmt.Errorf("failed to start container %d: %w", vmid, err)
	}

	if upid, ok := resp.(string); ok {
		return upid, nil
	}

	return "", fmt.Errorf("%w: %T", ErrUnexpectedResponseType, resp)
}

// Stop stops an LXC container.
func (c *Client) Stop(ctx context.Context, vmid int) (string, error) {
	path := fmt.Sprintf("/nodes/%s/lxc/%d/status/stop", url.PathEscape(c.node), vmid)

	resp, err := c.pveClient.PostCtx(ctx, path, nil)
	if err != nil {
		return "", fmt.Errorf("failed to stop container %d: %w", vmid, err)
	}

	if upid, ok := resp.(string); ok {
		return upid, nil
	}

	return "", fmt.Errorf("%w: %T", ErrUnexpectedResponseType, resp)
}

// Shutdown gracefully shuts down an LXC container.
func (c *Client) Shutdown(ctx context.Context, vmid int, timeout int) (string, error) {
	path := fmt.Sprintf("/nodes/%s/lxc/%d/status/shutdown", url.PathEscape(c.node), vmid)

	params := map[string]interface{}{}
	if timeout > 0 {
		params["timeout"] = timeout
	}

	resp, err := c.pveClient.PostCtx(ctx, path, params)
	if err != nil {
		return "", fmt.Errorf("failed to shutdown container %d: %w", vmid, err)
	}

	if upid, ok := resp.(string); ok {
		return upid, nil
	}

	return "", fmt.Errorf("%w: %T", ErrUnexpectedResponseType, resp)
}

// Reboot reboots an LXC container.
func (c *Client) Reboot(ctx context.Context, vmid int) (string, error) {
	path := fmt.Sprintf("/nodes/%s/lxc/%d/status/reboot", url.PathEscape(c.node), vmid)

	resp, err := c.pveClient.PostCtx(ctx, path, nil)
	if err != nil {
		return "", fmt.Errorf("failed to reboot container %d: %w", vmid, err)
	}

	if upid, ok := resp.(string); ok {
		return upid, nil
	}

	return "", fmt.Errorf("%w: %T", ErrUnexpectedResponseType, resp)
}

// Delete deletes an LXC container.
func (c *Client) Delete(ctx context.Context, vmid int, purge bool) (string, error) {
	path := fmt.Sprintf("/nodes/%s/lxc/%d", url.PathEscape(c.node), vmid)

	params := map[string]interface{}{}
	if purge {
		params["purge"] = 1
	}

	resp, err := c.pveClient.DeleteCtx(ctx, path, params)
	if err != nil {
		return "", fmt.Errorf("failed to delete container %d: %w", vmid, err)
	}

	if upid, ok := resp.(string); ok {
		return upid, nil
	}

	return "", fmt.Errorf("%w: %T", ErrUnexpectedResponseType, resp)
}

// GetConfig retrieves the configuration of an LXC container.
func (c *Client) GetConfig(ctx context.Context, vmid int) (map[string]interface{}, error) {
	path := fmt.Sprintf("/nodes/%s/lxc/%d/config", url.PathEscape(c.node), vmid)

	resp, err := c.pveClient.GetCtx(ctx, path, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to get container config: %w", err)
	}

	if config, ok := resp.(map[string]interface{}); ok {
		return config, nil
	}

	return nil, fmt.Errorf("%w: %T", ErrUnexpectedResponseType, resp)
}

// UpdateConfig updates the configuration of an LXC container.
func (c *Client) UpdateConfig(ctx context.Context, vmid int, config map[string]interface{}) error {
	path := fmt.Sprintf("/nodes/%s/lxc/%d/config", url.PathEscape(c.node), vmid)

	_, err := c.pveClient.PutCtx(ctx, path, config)
	if err != nil {
		return fmt.Errorf("failed to update container config: %w", err)
	}

	return nil
}

// Clone clones an LXC container.
func (c *Client) Clone(ctx context.Context, vmid int, newID int, opts CloneOptions) (string, error) {
	path := fmt.Sprintf("/nodes/%s/lxc/%d/clone", url.PathEscape(c.node), vmid)

	params := map[string]interface{}{
		"newid": newID,
	}

	if opts.Hostname != "" {
		params["hostname"] = opts.Hostname
	}

	if opts.Description != "" {
		params["description"] = opts.Description
	}

	if opts.Target != "" {
		params["target"] = opts.Target
	}

	if opts.Pool != "" {
		params["pool"] = opts.Pool
	}

	if opts.Storage != "" {
		params["storage"] = opts.Storage
	}

	if opts.Full {
		params["full"] = 1
	}

	resp, err := c.pveClient.PostCtx(ctx, path, params)
	if err != nil {
		return "", fmt.Errorf("failed to clone container %d: %w", vmid, err)
	}

	if upid, ok := resp.(string); ok {
		return upid, nil
	}

	return "", fmt.Errorf("%w: %T", ErrUnexpectedResponseType, resp)
}

// CloneOptions represents options for cloning a container.
type CloneOptions struct {
	Hostname    string
	Description string
	Target      string // Target node
	Pool        string // Resource pool
	Storage     string // Target storage
	Full        bool   // Full copy (not linked)
}

// Resize resizes a container disk.
func (c *Client) Resize(ctx context.Context, vmid int, disk string, size string) error {
	path := fmt.Sprintf("/nodes/%s/lxc/%d/resize", url.PathEscape(c.node), vmid)

	params := map[string]interface{}{
		"disk": disk,
		"size": size,
	}

	_, err := c.pveClient.PutCtx(ctx, path, params)
	if err != nil {
		return fmt.Errorf("failed to resize container disk: %w", err)
	}

	return nil
}

// Helper function to safely get string from map.
func getString(m map[string]interface{}, key string) string {
	if val, ok := m[key].(string); ok {
		return val
	}

	return ""
}
