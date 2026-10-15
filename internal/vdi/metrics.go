package vdi

import (
	"context"
	"net/http"
	"strconv"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
)

// Labels carry route patterns and fixed enums only; user or desktop IDs would explode cardinality.
var (
	httpRequests = promauto.NewCounterVec(prometheus.CounterOpts{Name: "vdi_http_requests_total", Help: "HTTP requests by route pattern and status code."}, []string{"route", "code"})
	httpDuration = promauto.NewHistogramVec(prometheus.HistogramOpts{Name: "vdi_http_request_duration_seconds", Help: "HTTP request latency by route pattern.", Buckets: prometheus.DefBuckets}, []string{"route"})

	loginsTotal         = promauto.NewCounterVec(prometheus.CounterOpts{Name: "vdi_logins_total", Help: "Login attempts by result."}, []string{"result"})
	createRequestsTotal = promauto.NewCounterVec(prometheus.CounterOpts{Name: "vdi_desktop_create_requests_total", Help: "Desktop create/assign requests accepted."}, []string{"action"})
	readyTotal          = promauto.NewCounter(prometheus.CounterOpts{Name: "vdi_desktop_ready_total", Help: "Desktops that became RUNNING+READY for the first time."})
	createFailuresTotal = promauto.NewCounterVec(prometheus.CounterOpts{Name: "vdi_desktop_create_failures_total", Help: "Desktop create failures by failure code."}, []string{"code"})
	createDuration      = promauto.NewHistogram(prometheus.HistogramOpts{Name: "vdi_desktop_create_duration_seconds", Help: "Seconds from create request to first RUNNING+READY.", Buckets: []float64{15, 30, 45, 60, 90, 120, 180, 300, 600}})
	connectsTotal       = promauto.NewCounterVec(prometheus.CounterOpts{Name: "vdi_desktop_connects_total", Help: "Remote connect requests by result."}, []string{"result"})
)

// Pre-create label values so outcomes that never happened still graph as 0.
func init() {
	for _, r := range []string{"success", "failure"} {
		loginsTotal.WithLabelValues(r)
		connectsTotal.WithLabelValues(r)
	}
	for _, a := range []string{"create", "assign"} {
		createRequestsTotal.WithLabelValues(a)
	}
}
func result(e error) string {
	if e != nil {
		return "failure"
	}
	return "success"
}

type statusRecorder struct {
	http.ResponseWriter
	code int
}

func (s *statusRecorder) WriteHeader(code int) { s.code = code; s.ResponseWriter.WriteHeader(code) }

func instrument(mux *http.ServeMux, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, route := mux.Handler(r)
		if route == "GET /metrics" {
			next.ServeHTTP(w, r)
			return
		}
		if route == "" {
			route = "unmatched"
		}
		rec := &statusRecorder{ResponseWriter: w, code: http.StatusOK}
		start := time.Now()
		next.ServeHTTP(rec, r)
		httpDuration.WithLabelValues(route).Observe(time.Since(start).Seconds())
		httpRequests.WithLabelValues(route, strconv.Itoa(rec.code)).Inc()
	})
}

// observeDesktopEvent must run once, after the transaction recording the event commits.
func observeDesktopEvent(event string, d Desktop, now time.Time) {
	switch event {
	case "DESKTOP_CREATE":
		createRequestsTotal.WithLabelValues("create").Inc()
	case "DESKTOP_ASSIGN":
		createRequestsTotal.WithLabelValues("assign").Inc()
	case "DESKTOP_READY":
		readyTotal.Inc()
		createDuration.Observe(now.Sub(d.CreatedAt).Seconds())
	case "DESKTOP_CREATE_FAILED":
		code := "UNKNOWN"
		if d.Failure != nil {
			code = d.Failure.Code
		}
		createFailuresTotal.WithLabelValues(code).Inc()
	}
}

// stateCollector counts MongoDB at scrape time, so restarts and multiple Pods report the same values.
type stateCollector struct{ store *Store }

var (
	desktopsDesc = prometheus.NewDesc("vdi_desktops", "Non-deleted desktops by status, connection state and node.", []string{"status", "connection_state", "node"}, nil)
	sessionsDesc = prometheus.NewDesc("vdi_active_sessions", "Unexpired login sessions.", nil, nil)
)

func NewStateCollector(s *Store) prometheus.Collector { return stateCollector{s} }
func (c stateCollector) Describe(ch chan<- *prometheus.Desc) {
	ch <- desktopsDesc
	ch <- sessionsDesc
}
func (c stateCollector) Collect(ch chan<- prometheus.Metric) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	cur, e := c.store.C("desktops").Aggregate(ctx, mongo.Pipeline{
		{{Key: "$match", Value: bson.M{"deleted": false}}},
		{{Key: "$group", Value: bson.M{"_id": bson.M{"status": "$status", "conn": "$connectionState", "node": "$nodeName"}, "count": bson.M{"$sum": 1}}}},
	})
	if e != nil {
		ch <- prometheus.NewInvalidMetric(desktopsDesc, e)
		return
	}
	var rows []struct {
		ID struct {
			Status string  `bson:"status"`
			Conn   string  `bson:"conn"`
			Node   *string `bson:"node"`
		} `bson:"_id"`
		Count int64 `bson:"count"`
	}
	if e = cur.All(ctx, &rows); e != nil {
		ch <- prometheus.NewInvalidMetric(desktopsDesc, e)
		return
	}
	for _, r := range rows {
		node := ""
		if r.ID.Node != nil {
			node = *r.ID.Node
		}
		ch <- prometheus.MustNewConstMetric(desktopsDesc, prometheus.GaugeValue, float64(r.Count), r.ID.Status, r.ID.Conn, node)
	}
	n, e := c.store.C("sessions").CountDocuments(ctx, bson.M{"expiresAt": bson.M{"$gt": time.Now().UTC()}})
	if e != nil {
		ch <- prometheus.NewInvalidMetric(sessionsDesc, e)
		return
	}
	ch <- prometheus.MustNewConstMetric(sessionsDesc, prometheus.GaugeValue, float64(n))
}
