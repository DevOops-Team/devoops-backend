package telemetry

import (
	"bytes"
	"context"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"go.opentelemetry.io/otel"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	"go.opentelemetry.io/otel/trace"
)

func TestSetupNoopWithoutEndpoint(t *testing.T) {
	t.Setenv("OTEL_EXPORTER_OTLP_ENDPOINT", "")
	shutdown, err := Setup(context.Background())
	if err != nil || shutdown(context.Background()) != nil {
		t.Fatal("no-op setup failed")
	}
}
func TestSetupWithEndpointDoesNotBlock(t *testing.T) {
	t.Setenv("OTEL_EXPORTER_OTLP_ENDPOINT", "http://127.0.0.1:1")
	shutdown, err := Setup(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 0)
	defer cancel()
	_ = shutdown(ctx)
}
func TestSpanNameRouteAndTraceToWorker(t *testing.T) {
	t.Setenv("OTEL_EXPORTER_OTLP_ENDPOINT", "")
	Setup(context.Background())
	rec := tracetest.NewSpanRecorder()
	otel.SetTracerProvider(sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(rec)))
	var saved string
	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/desktops", func(w http.ResponseWriter, r *http.Request) {
		saved = Inject(r.Context())
		w.WriteHeader(202)
	})
	mux.HandleFunc("GET /metrics", func(w http.ResponseWriter, r *http.Request) {})
	h := Handler(mux, mux)
	h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest("POST", "/api/desktops", nil))
	h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest("GET", "/metrics", nil))
	_, span := otel.Tracer("worker").Start(Extract(context.Background(), saved), "desktop.reconcile")
	span.End()
	var buf bytes.Buffer
	slog.New(LogHandler{slog.NewJSONHandler(&buf, nil)}).InfoContext(trace.ContextWithSpanContext(context.Background(), span.SpanContext()), "worker tick")
	spans := rec.Ended()
	if len(spans) != 2 || spans[0].Name() != "POST /api/desktops" {
		t.Fatalf("spans = %d (want request + worker, /metrics filtered)", len(spans))
	}
	if spans[1].Parent().TraceID() != spans[0].SpanContext().TraceID() {
		t.Fatal("worker span is not in the request trace")
	}
	if !strings.Contains(buf.String(), spans[0].SpanContext().TraceID().String()) {
		t.Fatal("trace_id missing in log")
	}
}
