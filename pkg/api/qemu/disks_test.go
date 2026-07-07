package qemu_test

import (
	"context"
	"testing"

	"github.com/fivetwenty-io/proxmox-apiclient-go/v3/pkg/api/qemu"
)

func TestAttachDiskInvalidBus(t *testing.T) {
	t.Parallel()

	svc := qemu.New(nil)

	_, err := svc.AttachDisk(context.Background(), "n1", 100, "vol", "pcie", nil)
	if err == nil {
		t.Fatalf("expected error for invalid bus")
	}
}
