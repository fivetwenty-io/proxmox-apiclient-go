package cluster_test

import (
	"context"
	"net/http"
	"testing"

	"github.com/fivetwenty-io/proxmox-apiclient-go/v3/pkg/api/cluster"
	pveclient "github.com/fivetwenty-io/proxmox-apiclient-go/v3/pkg/client"
)

//nolint:ireturn // test helper returns the Service interface that cluster.New returns
func newClusterService(t *testing.T) (cluster.Service, *testHarness) {
	t.Helper()

	srv, harness := newTestHarness()
	t.Cleanup(srv.Close)

	apiClient, err := pveclient.NewClient(smokeOptsFromServerURL(srv.URL))
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}

	return cluster.New(apiClient), harness
}

// Since PVE 9.3 the HA migrate parameters are a oneOf over the resource
// types, discriminated by resource-type, which the server derives from the
// service ID. A caller that sets only the target node must not send it.
func TestCreateHaResourcesMigrateOmitsUnsetResourceType(t *testing.T) {
	t.Parallel()

	svc, harness := newClusterService(t)
	harness.set(http.StatusOK, `{"data":{"sid":"vm:100","requested-node":"pve2"},"success":1}`)

	conntrack := true

	resp, err := svc.CreateHaResourcesMigrate(context.Background(), "vm:100", &cluster.CreateHaResourcesMigrateParams{
		Node:               "pve2",
		WithConntrackState: &conntrack,
	})
	if err != nil {
		t.Fatalf("CreateHaResourcesMigrate: unexpected error: %v", err)
	}

	if resp.Sid != "vm:100" || resp.RequestedNode != "pve2" {
		t.Errorf("response = %+v, want sid vm:100 requested on pve2", resp)
	}

	got := harness.snapshot()
	assertRequestLine(t, got, http.MethodPost, "/api2/json/cluster/ha/resources/vm:100/migrate")
	assertParamValue(t, got.form, "node", "pve2")
	assertParamValue(t, got.form, "with-conntrack-state", "1")

	for _, key := range []string{"resource-type", "sid"} {
		if _, ok := got.form[key]; ok {
			t.Errorf("migrate sent %s = %q; want it omitted", key, got.form.Get(key))
		}
	}
}

func TestCreateHaResourcesMigrateSendsResourceTypeWhenSet(t *testing.T) {
	t.Parallel()

	svc, harness := newClusterService(t)
	harness.set(http.StatusOK, `{"data":{"sid":"ct:200","requested-node":"pve3"},"success":1}`)

	resourceType := "ct"

	_, err := svc.CreateHaResourcesMigrate(context.Background(), "ct:200", &cluster.CreateHaResourcesMigrateParams{
		Node:         "pve3",
		ResourceType: &resourceType,
	})
	if err != nil {
		t.Fatalf("CreateHaResourcesMigrate: unexpected error: %v", err)
	}

	assertParamValue(t, harness.snapshot().form, "resource-type", "ct")
}
