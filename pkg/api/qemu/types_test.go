package qemu_test

import (
	"testing"

	"github.com/fivetwenty-io/proxmox-apiclient-go/v3/pkg/api/qemu"
)

const volIDDisk9003 = "data:vm-9003-disk-0"

// TestFindDiskIDByVolID_OptionStringTolerant exercises the common PVE config
// shape where disk values carry a comma-separated option suffix
// ("data:vm-9003-disk-0,size=64G"). Prior to the tolerance fix, a caller
// asking for the bare volid would miss the entry and treat the disk as not
// attached.
func TestFindDiskIDByVolID_OptionStringTolerant(t *testing.T) {
	t.Parallel()

	cases := buildFindDiskIDCases()

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()

			gotID, gotHit := qemu.FindDiskIDByVolID(testCase.cfg, testCase.volid)
			if gotHit != testCase.wantHit {
				t.Errorf("hit: got %v, want %v", gotHit, testCase.wantHit)
			}

			if gotID != testCase.wantID {
				t.Errorf("id:  got %q, want %q", gotID, testCase.wantID)
			}
		})
	}
}

type findDiskCase struct {
	name    string
	cfg     map[string]interface{}
	volid   string
	wantID  string
	wantHit bool
}

func buildFindDiskIDCases() []findDiskCase {
	return []findDiskCase{
		{
			name: "exact match (no options)",
			cfg: map[string]interface{}{
				diskScsi1: volIDDisk9003,
			},
			volid:   volIDDisk9003,
			wantID:  diskScsi1,
			wantHit: true,
		},
		{
			name: "option-string form (size suffix)",
			cfg: map[string]interface{}{
				diskScsi1: volIDDisk9003 + ",size=64G",
			},
			volid:   volIDDisk9003,
			wantID:  diskScsi1,
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
				diskScsi1: volIDDisk9003 + "-extra,size=64G",
			},
			volid:   volIDDisk9003,
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
				keyName: volIDDisk9003,
				"scsi2": volIDDisk9003 + ",size=64G",
			},
			volid:   volIDDisk9003,
			wantID:  "scsi2",
			wantHit: true,
		},
	}
}
