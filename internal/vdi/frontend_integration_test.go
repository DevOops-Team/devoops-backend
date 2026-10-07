package vdi_test

import (
	"context"
	"devoops/vdi/internal/testinfra"
	"errors"
	"fmt"
	"net"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

func TestImageSpecForCreateForm(t *testing.T) {
	f := setup(t)
	path := fmt.Sprintf("/api/images/%d/spec", f.osID)
	v := f.request("GET", path, f.admin, nil, "", 200)
	keys(t, v, "osId", "cpuCores", "memoryGb", "storageGb")
	if v["cpuCores"] != float64(1) || v["memoryGb"] != float64(1) || v["storageGb"] != float64(10) {
		t.Fatal(v)
	}
	body := map[string]any{"name": "from-server-spec", "osId": v["osId"], "cpuCores": v["cpuCores"], "memoryGb": v["memoryGb"], "storageGb": v["storageGb"]}
	f.request("POST", "/api/desktops", f.admin, body, "990b76a8-972f-46f4-a7ce-478576e617bd", 202)
	f.request("GET", path, "", nil, "", 401)
	f.request("GET", "/api/images/invalid/spec", f.admin, nil, "", 400)
	f.request("GET", "/api/images/9007199254740991/spec", f.admin, nil, "", 404)
	f.request("GET", path+"?extra=1", f.admin, nil, "", 400)
	f.cloud.Set(func(s *testinfra.OpenStack) { s.RAM = 512 })
	f.request("GET", path, f.admin, nil, "", 409)
	f.cloud.Set(func(s *testinfra.OpenStack) { s.RAM = 1024; s.ImageStatus = "queued" })
	f.request("GET", path, f.admin, nil, "", 409)
}

// Opt-in because this test starts the sibling Next.js production server.
// Build frontend first with VDI_BACKEND_URL=http://127.0.0.1:8080.
func TestFrontendIntegration(t *testing.T) {
	dir := os.Getenv("FRONTEND_INTEGRATION_DIR")
	if dir == "" {
		t.Skip("FRONTEND_INTEGRATION_DIR required for the Next.js integration test")
	}
	dir, e := filepath.Abs(dir)
	if e != nil {
		t.Fatal(e)
	}
	f := setup(t)
	f.app.Now = time.Now
	f.worker.Now = time.Now
	f.worker.Interval = 20 * time.Millisecond
	f.cloud.Set(func(s *testinfra.OpenStack) { s.ReadyRDP = true })
	f.server.Close()
	listener, e := net.Listen("tcp", "127.0.0.1:8080")
	if e != nil {
		t.Fatalf("frontend build expects a dedicated test backend on :8080: %v", e)
	}
	f.server = httptest.NewUnstartedServer(f.app.Handler())
	f.server.Listener.Close()
	f.server.Listener = listener
	f.server.Start()
	t.Cleanup(f.server.Close)
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- f.worker.Run(ctx) }()
	t.Cleanup(func() {
		cancel()
		if e := <-done; e != nil && !errors.Is(e, context.Canceled) {
			t.Error(e)
		}
	})
	cmd := exec.CommandContext(ctx, "node", "../../scripts/test-frontend.cjs", dir)
	output, e := cmd.CombinedOutput()
	t.Log(string(output))
	if e != nil {
		t.Fatalf("frontend integration: %v", e)
	}
}
