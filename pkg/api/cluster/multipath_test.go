package cluster_test

import (
	"context"
	"net/http"
	"testing"

	"github.com/fivetwenty-io/proxmox-apiclient-go/v3/pkg/api/cluster"
)

// testWwid is a SCSI NAA WWID in the form multipathd reports for a LUN.
const testWwid = "3600a098038304437415d4b6a59684a52"

func TestCreateMultipathWwidSendsMapSettings(t *testing.T) {
	t.Parallel()

	svc, harness := newClusterService(t)
	harness.set(http.StatusOK, `{"data":null,"success":1}`)

	alias := "san-lun0"
	paths := int64(4)

	err := svc.CreateMultipathWwid(context.Background(), &cluster.CreateMultipathWwidParams{
		Wwid:          testWwid,
		Alias:         &alias,
		PathsExpected: &paths,
	})
	if err != nil {
		t.Fatalf("CreateMultipathWwid: unexpected error: %v", err)
	}

	got := harness.snapshot()
	assertRequestLine(t, got, http.MethodPost, "/api2/json/cluster/multipath/wwid")
	assertParamValue(t, got.form, "wwid", testWwid)
	assertParamValue(t, got.form, "alias", alias)
	assertParamValue(t, got.form, "paths-expected", "4")
}

func TestGetMultipathWwidDecodesMap(t *testing.T) {
	t.Parallel()

	svc, harness := newClusterService(t)
	harness.set(http.StatusOK, `{"data":{"wwid":"`+testWwid+`","alias":"san-lun0","paths-expected":4,`+
		`"no-path-retry":"queue","digest":"9b1f0c"},"success":1}`)

	resp, err := svc.GetMultipathWwid(context.Background(), testWwid)
	if err != nil {
		t.Fatalf("GetMultipathWwid: unexpected error: %v", err)
	}

	assertRequestLine(t, harness.snapshot(), http.MethodGet, "/api2/json/cluster/multipath/wwid/"+testWwid)

	if resp.Wwid != testWwid || resp.Alias == nil || *resp.Alias != "san-lun0" {
		t.Errorf("response = %+v, want wwid %s with alias san-lun0", resp, testWwid)
	}

	if resp.PathsExpected == nil || int64(*resp.PathsExpected) != 4 {
		t.Errorf("PathsExpected = %v, want 4", resp.PathsExpected)
	}

	if resp.NoPathRetry == nil || *resp.NoPathRetry != "queue" {
		t.Errorf("NoPathRetry = %v, want queue", resp.NoPathRetry)
	}
}
