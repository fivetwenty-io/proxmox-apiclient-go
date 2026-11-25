package clusterstorage_test

import (
	"context"
	"net/http"
	"testing"

	"github.com/fivetwenty-io/proxmox-apiclient-go/v3/pkg/api/clusterstorage"
	pveclient "github.com/fivetwenty-io/proxmox-apiclient-go/v3/pkg/client"
)

// Since PVE 9.3 both storage endpoints declare their parameters as an
// allOf with one oneOf variant per storage type, discriminated by "type".
// Create must carry the type to pick the variant, while update must not
// need it, because pve-storage resolves the type from the existing
// storage configuration.

//nolint:ireturn // test helper returns the Service interface that clusterstorage.New returns
func newStorageService(t *testing.T) (clusterstorage.Service, *testHarness) {
	t.Helper()

	srv, harness := newTestHarness()
	t.Cleanup(srv.Close)

	apiClient, err := pveclient.NewClient(smokeOptsFromServerURL(srv.URL))
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}

	return clusterstorage.New(apiClient), harness
}

func TestCreateStorageSendsTypeAndVariantFields(t *testing.T) {
	t.Parallel()

	svc, harness := newStorageService(t)
	harness.set(http.StatusOK, `{"data":{"storage":"san1","type":"nvmeof"},"success":1}`)

	portals := "192.0.2.10,[2001:db8::10]:4420"
	subsysnqn := "nqn.2014-08.org.example:san1"

	resp, err := svc.CreateStorage(context.Background(), &clusterstorage.CreateStorageParams{
		Storage:   "san1",
		Type:      "nvmeof",
		Portals:   &portals,
		Subsysnqn: &subsysnqn,
	})
	if err != nil {
		t.Fatalf("CreateStorage: unexpected error: %v", err)
	}

	if resp.Storage != "san1" || resp.Type != "nvmeof" {
		t.Errorf("response = %+v, want storage san1 of type nvmeof", resp)
	}

	got := harness.snapshot()
	assertRequestLine(t, got, http.MethodPost, "/api2/json/storage")
	assertParamValue(t, got.form, "storage", "san1")
	assertParamValue(t, got.form, "type", "nvmeof")
	assertParamValue(t, got.form, "portals", portals)
	assertParamValue(t, got.form, "subsysnqn", subsysnqn)
}

func TestUpdateStorageOmitsUnsetType(t *testing.T) {
	t.Parallel()

	svc, harness := newStorageService(t)
	harness.set(http.StatusOK, `{"data":{"storage":"local","type":"dir"},"success":1}`)

	content := "iso,vztmpl,backup"

	_, err := svc.UpdateStorage(context.Background(), "local", &clusterstorage.UpdateStorageParams{Content: &content})
	if err != nil {
		t.Fatalf("UpdateStorage: unexpected error: %v", err)
	}

	got := harness.snapshot()
	assertRequestLine(t, got, http.MethodPut, "/api2/json/storage/local")
	assertParamValue(t, got.form, "content", content)

	if _, ok := got.form["type"]; ok {
		t.Errorf("update sent type = %q; want it omitted so the server resolves it", got.form.Get("type"))
	}
}

func TestUpdateStorageSendsTypeWhenSet(t *testing.T) {
	t.Parallel()

	svc, harness := newStorageService(t)
	harness.set(http.StatusOK, `{"data":{"storage":"local","type":"dir"},"success":1}`)

	storageType := "dir"

	_, err := svc.UpdateStorage(context.Background(), "local", &clusterstorage.UpdateStorageParams{Type: &storageType})
	if err != nil {
		t.Fatalf("UpdateStorage: unexpected error: %v", err)
	}

	assertParamValue(t, harness.snapshot().form, "type", "dir")
}

func TestDeleteStorageSendsDigest(t *testing.T) {
	t.Parallel()

	svc, harness := newStorageService(t)
	harness.set(http.StatusOK, `{"data":null,"success":1}`)

	digest := "4f2c0d3e8b1a"

	err := svc.DeleteStorage(context.Background(), "san1", &clusterstorage.DeleteStorageParams{Digest: &digest})
	if err != nil {
		t.Fatalf("DeleteStorage: unexpected error: %v", err)
	}

	got := harness.snapshot()
	assertRequestLine(t, got, http.MethodDelete, "/api2/json/storage/san1")
	assertParamValue(t, got.query, "digest", digest)
}
