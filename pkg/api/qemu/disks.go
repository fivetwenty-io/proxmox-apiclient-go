package qemu

import (
	"context"
	"errors"
	"fmt"
	"strings"
)

var (
	errUnsupportedBus = errors.New("unsupported bus")
	errSizeNonZero    = errors.New("sizeGiB must be non-zero")
)

// AttachOpts controls disk attach behavior.
type AttachOpts struct {
	// DiskID optionally sets the exact disk key (e.g., "scsi1").
	DiskID string
	// Extra allows additional VM config parameters alongside the disk assignment.
	Extra map[string]interface{}
}

// AttachDisk attaches a volume to a VM, computing the next index for the bus if needed.
func (s *service) AttachDisk(ctx context.Context, node string, vmid int, volid string, bus string, opts *AttachOpts) (string, error) {
	err := s.validateBus(bus)
	if err != nil {
		return "", err
	}

	diskID, err := s.determineDiskID(ctx, node, vmid, volid, strings.ToLower(bus), opts)
	if err != nil {
		return "", err
	}

	params := s.buildAttachParams(diskID, volid, opts)

	err = s.attachDiskToVM(ctx, node, vmid, params)
	if err != nil {
		return "", err
	}

	return diskID, nil
}

func (s *service) validateBus(bus string) error {
	bus = strings.ToLower(bus)
	switch bus {
	case "scsi", "virtio", "ide", "sata":
		return nil
	default:
		return fmt.Errorf("%w: %s", errUnsupportedBus, bus)
	}
}

func (s *service) determineDiskID(ctx context.Context, node string, vmid int, volid, bus string, opts *AttachOpts) (string, error) {
	if opts != nil && opts.DiskID != "" {
		return opts.DiskID, nil
	}

	cfg, err := s.Config(ctx, node, vmid)
	if err != nil {
		return "", err
	}

	if existing, ok := FindDiskIDByVolID(cfg, volid); ok {
		return existing, nil
	}

	return fmt.Sprintf("%s%d", bus, NextIndexForBus(cfg, bus)), nil
}

func (s *service) buildAttachParams(diskID, volid string, opts *AttachOpts) map[string]interface{} {
	params := map[string]interface{}{diskID: volid}

	if opts != nil && opts.Extra != nil {
		for k, v := range opts.Extra {
			params[k] = v
		}
	}

	return params
}

func (s *service) attachDiskToVM(ctx context.Context, node string, vmid int, params map[string]interface{}) error {
	_, err := s.c.PutCtx(ctx, fmt.Sprintf("/nodes/%s/qemu/%d/config", node, vmid), params)
	if err != nil {
		return fmt.Errorf("failed to attach disk to VM %d on node %q: %w", vmid, node, err)
	}

	return nil
}

// DetachDisk detaches a disk by its diskID (e.g., scsi0).
func (s *service) DetachDisk(ctx context.Context, node string, vmid int, diskID string) error {
	cfg, err := s.Config(ctx, node, vmid)
	if err != nil {
		return err
	}

	if _, ok := cfg[diskID]; !ok {
		return nil
	}

	params := map[string]interface{}{"delete": diskID}

	_, err = s.c.PutCtx(ctx, fmt.Sprintf("/nodes/%s/qemu/%d/config", node, vmid), params)
	if err != nil {
		return fmt.Errorf("failed to detach disk %q from VM %d on node %q: %w", diskID, vmid, node, err)
	}

	return nil
}

// ResizeDisk resizes a disk and returns the UPID.
func (s *service) ResizeDisk(ctx context.Context, node string, vmid int, diskID string, sizeGiB int) (string, error) {
	if sizeGiB == 0 {
		// No-op resize still calls API, but guard against accidental zero.
		return "", errSizeNonZero
	}

	params := map[string]interface{}{
		"disk": diskID,
		"size": fmt.Sprintf("+%dG", sizeGiB),
	}

	// PVE's /resize endpoint uses PUT.
	data, err := s.c.PutCtx(ctx, fmt.Sprintf("/nodes/%s/qemu/%d/resize", node, vmid), params)
	if err != nil {
		return "", fmt.Errorf("failed to execute QEMU operation: %w", err)
	}
	if upid, ok := data.(string); ok {
		return upid, nil
	}
	if m, ok := data.(map[string]interface{}); ok {
		if v, ok := m["upid"].(string); ok {
			return v, nil
		}
	}
	return "", nil
}
