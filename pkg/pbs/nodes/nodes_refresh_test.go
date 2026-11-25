package nodes_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"testing"

	pveclient "github.com/fivetwenty-io/proxmox-apiclient-go/v3/pkg/client"
	"github.com/fivetwenty-io/proxmox-apiclient-go/v3/pkg/pbs/nodes"
)

// capturedRequest holds the parts of the last request the mock server saw.
type capturedRequest struct {
	method string
	path   string
	query  url.Values
	form   url.Values
}

//nolint:ireturn // Test helper returns the package public interface.
func newRefreshService(t *testing.T, body string, seen *capturedRequest) nodes.Service {
	t.Helper()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		*seen = capturedRequest{method: r.Method, path: r.URL.Path, query: r.URL.Query(), form: r.PostForm}

		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)

	parsed, err := url.Parse(srv.URL)
	if err != nil {
		t.Fatalf("parse server URL: %v", err)
	}

	port, _ := strconv.Atoi(parsed.Port())

	client, err := pveclient.NewClient(pveclient.Options{
		Host: parsed.Hostname(), Port: port, Protocol: "http", APIToken: "user@pam!tok=sec",
	})
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}

	return nodes.New(client)
}

func TestListAccessLogDecodesPageAndSendsFilters(t *testing.T) {
	t.Parallel()

	var seen capturedRequest

	svc := newRefreshService(t, `{"data":{"complete":true,"entries":[{"time":1790000000}],"gaps":[],`+
		`"head-cursor":"h1","invalid-lines":2,"next-cursor":"n1","searched-back-to":1789999000},"success":1}`, &seen)

	limit := int64(50)
	method := "GET"

	resp, err := svc.ListAccessLog(context.Background(), "pbs1", &nodes.ListAccessLogParams{Limit: &limit, Method: &method})
	if err != nil {
		t.Fatalf("ListAccessLog: %v", err)
	}

	if seen.path != "/api2/json/nodes/pbs1/access-log" {
		t.Errorf("path = %q", seen.path)
	}

	if seen.query.Get("limit") != "50" || seen.query.Get("method") != "GET" {
		t.Errorf("query = %v", seen.query)
	}

	if !bool(resp.Complete) || len(resp.Entries) != 1 || resp.InvalidLines.Int() != 2 {
		t.Errorf("unexpected response: %+v", resp)
	}

	if resp.HeadCursor == nil || *resp.HeadCursor != "h1" || resp.NextCursor == nil || *resp.NextCursor != "n1" {
		t.Errorf("cursors not decoded: %+v", resp)
	}

	if resp.SearchedBackTo == nil || resp.SearchedBackTo.Int() != 1789999000 {
		t.Errorf("searched-back-to not decoded: %+v", resp.SearchedBackTo)
	}
}

func TestListAptHistoryAndTermlogRequests(t *testing.T) {
	t.Parallel()

	var seen capturedRequest

	svc := newRefreshService(t, `{"data":[{"start-time":1790000000,"packages":[{"name":"vim","action":"upgrade"}]}],"success":1}`, &seen)

	pkg := "vim"

	resp, err := svc.ListAptHistory(context.Background(), "pbs1", &nodes.ListAptHistoryParams{Package: &pkg})
	if err != nil {
		t.Fatalf("ListAptHistory: %v", err)
	}

	if len(*resp) != 1 || seen.query.Get("package") != "vim" {
		t.Errorf("history = %v, query = %v", *resp, seen.query)
	}

	svc = newRefreshService(t, `{"data":"Start-Date: 2026-11-01\n","success":1}`, &seen)

	_, err = svc.ListAptTermlog(context.Background(), "pbs1", &nodes.ListAptTermlogParams{StartTime: 1790000000})
	if err != nil {
		t.Fatalf("ListAptTermlog: %v", err)
	}

	if seen.path != "/api2/json/nodes/pbs1/apt/termlog" || seen.query.Get("start-time") != "1790000000" {
		t.Errorf("termlog request = %s ?%v", seen.path, seen.query)
	}
}

func TestCreateDisksZfsSendsRecordsize(t *testing.T) {
	t.Parallel()

	var seen capturedRequest

	svc := newRefreshService(t, `{"data":"UPID:pbs1:1","success":1}`, &seen)

	recordsize := "128k"

	_, err := svc.CreateDisksZfs(context.Background(), "pbs1", &nodes.CreateDisksZfsParams{
		Name: "tank", Devices: "sdb,sdc", Raidlevel: "mirror", Recordsize: &recordsize,
	})
	if err != nil {
		t.Fatalf("CreateDisksZfs: %v", err)
	}

	if seen.method != http.MethodPost || seen.form.Get("recordsize") != "128k" {
		t.Errorf("method = %s, form = %v", seen.method, seen.form)
	}
}
