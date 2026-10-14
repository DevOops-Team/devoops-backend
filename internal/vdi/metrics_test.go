package vdi

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus/promhttp"
	"github.com/prometheus/client_golang/prometheus/testutil"
)

func TestInstrumentUsesRoutePattern(t *testing.T) {
	mux := http.NewServeMux()
	mux.Handle("GET /metrics", promhttp.Handler())
	mux.HandleFunc("GET /api/desktops/{desktopId}", func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(404) })
	h := instrument(mux, mux)
	route := httpRequests.WithLabelValues("GET /api/desktops/{desktopId}", "404")
	unmatched := httpRequests.WithLabelValues("unmatched", "404")
	beforeRoute, beforeUnmatched := testutil.ToFloat64(route), testutil.ToFloat64(unmatched)
	for _, p := range []string{"/api/desktops/5", "/api/desktops/77", "/nope"} {
		h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest("GET", p, nil))
	}
	if v := testutil.ToFloat64(route) - beforeRoute; v != 2 {
		t.Fatalf("route pattern count = %v, want 2", v)
	}
	if v := testutil.ToFloat64(unmatched) - beforeUnmatched; v != 1 {
		t.Fatalf("unmatched = %v, want 1", v)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", "/metrics", nil))
	body := rec.Body.String()
	if !strings.Contains(body, `vdi_logins_total{result="failure"}`) || strings.Contains(body, `route="GET /metrics"`) {
		t.Fatal("metrics output unexpected")
	}
}
func TestObserveDesktopEvent(t *testing.T) {
	ready, assign, failed := testutil.ToFloat64(readyTotal), testutil.ToFloat64(createRequestsTotal.WithLabelValues("assign")), testutil.ToFloat64(createFailuresTotal.WithLabelValues("RDP_READY_TIMEOUT"))
	now := time.Now()
	observeDesktopEvent("DESKTOP_READY", Desktop{DesktopResponse: DesktopResponse{CreatedAt: now.Add(-45 * time.Second)}}, now)
	observeDesktopEvent("DESKTOP_ASSIGN", Desktop{}, now)
	observeDesktopEvent("DESKTOP_CREATE_FAILED", Desktop{DesktopResponse: DesktopResponse{Failure: &Failure{Code: "RDP_READY_TIMEOUT"}}}, now)
	observeDesktopEvent("DESKTOP_DELETE", Desktop{}, now)
	if testutil.ToFloat64(readyTotal)-ready != 1 || testutil.ToFloat64(createRequestsTotal.WithLabelValues("assign"))-assign != 1 || testutil.ToFloat64(createFailuresTotal.WithLabelValues("RDP_READY_TIMEOUT"))-failed != 1 {
		t.Fatal("event counters")
	}
	if testutil.CollectAndCount(createDuration) != 1 {
		t.Fatal("duration histogram")
	}
}
