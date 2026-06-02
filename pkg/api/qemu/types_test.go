package qemu_test

import (
	"testing"

	"github.com/fivetwenty-io/pve-apiclient-go/v3/pkg/api/qemu"
)

// TestFindDiskIDByVolID_OptionStringTolerant exercises the common PVE config
// shape where disk values carry a comma-separated option suffix
// ("data:vm-9003-disk-0,size=64G"). Prior to the tolerance fix, a caller
// asking for the bare volid would miss the entry and treat the disk as not
// attached.
func TestFindDiskIDByVolID_OptionStringTolerant(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name    string
		cfg     map[string]interface{}
		volid   string
		wantID  string
		wantHit bool
	}{
		{
			name: "exact match (no options)",
			cfg: map[string]interface{}{
				"scsi1": "data:vm-9003-disk-0",
			},
			volid:   "data:vm-9003-disk-0",
			wantID:  "scsi1",
			wantHit: true,
		},
		{
			name: "option-string form (size suffix)",
			cfg: map[string]interface{}{
				"scsi1": "data:vm-9003-disk-0,size=64G",
			},
			volid:   "data:vm-9003-disk-0",
			wantID:  "scsi1",
			wantHit: true,
		},
		{
			name: "option-string form with multiple opts",
			cfg: map[string]interface{}{
				"virtio0": "local-lvm:vm-100-disk-0,format=qcow2,size=20G,iothread=1",
			},
			volid:   "local-lvm:vm-100-disk-0",
			wantID:  "virtio0",
			wantHit: true,
		},
		{
			name: "prefix-only must not match (different volid)",
			cfg: map[string]interface{}{
				"scsi1": "data:vm-9003-disk-0-extra,size=64G",
			},
			volid:   "data:vm-9003-disk-0",
			wantID:  "",
			wantHit: false,
		},
		{
			name:    "empty cfg",
			cfg:     map[string]interface{}{},
			volid:   "data:vm-1-disk-0",
			wantID:  "",
			wantHit: false,
		},
		{
			name: "skips non-disk keys",
			cfg: map[string]interface{}{
				"name":  "data:vm-9003-disk-0",
				"scsi2": "data:vm-9003-disk-0,size=64G",
			},
			volid:   "data:vm-9003-disk-0",
			wantID:  "scsi2",
			wantHit: true,
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()

			gotID, gotHit := qemu.FindDiskIDByVolID(c.cfg, c.volid)
			if gotHit != c.wantHit {
				t.Errorf("hit: got %v, want %v", gotHit, c.wantHit)
			}

			if gotID != c.wantID {
				t.Errorf("id:  got %q, want %q", gotID, c.wantID)
			}
		})
	}
}
