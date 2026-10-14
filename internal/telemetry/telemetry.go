// Package telemetry configures OpenTelemetry tracing. Metrics are exposed only through Prometheus (/metrics).
package telemetry

import (
	"context"
	"log/slog"
	"net/http"
	"os"

	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracegrpc"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/trace"
)

// Setup is a no-op without OTEL_EXPORTER_OTLP_ENDPOINT, so it is safe to deploy before the collector.
// Other settings use the standard OTEL_SERVICE_NAME, OTEL_TRACES_SAMPLER and OTEL_TRACES_SAMPLER_ARG variables.
func Setup(ctx context.Context) (func(context.Context) error, error) {
	otel.SetTextMapPropagator(propagation.NewCompositeTextMapPropagator(propagation.TraceContext{}, propagation.Baggage{}))
	if os.Getenv("OTEL_EXPORTER_OTLP_ENDPOINT") == "" {
		return func(context.Context) error { return nil }, nil
	}
	exp, err := otlptracegrpc.New(ctx)
	if err != nil {
		return nil, err
	}
	res, err := resource.Merge(resource.Default(), resource.Environment())
	if err != nil {
		return nil, err
	}
	tp := sdktrace.NewTracerProvider(sdktrace.WithBatcher(exp), sdktrace.WithResource(res))
	otel.SetTracerProvider(tp)
	return tp.Shutdown, nil
}

// Handler names each incoming span by its route pattern and skips /metrics.
func Handler(mux *http.ServeMux, next http.Handler) http.Handler {
	return otelhttp.NewHandler(next, "http",
		otelhttp.WithSpanNameFormatter(func(_ string, r *http.Request) string {
			if _, p := mux.Handler(r); p != "" {
				return p
			}
			return r.Method + " unmatched"
		}),
		otelhttp.WithFilter(func(r *http.Request) bool { return r.URL.Path != "/metrics" }),
	)
}

// Transport adds spans and traceparent headers to outgoing HTTP calls.
func Transport(base http.RoundTripper) http.RoundTripper {
	if base == nil {
		base = http.DefaultTransport
	}
	return otelhttp.NewTransport(base)
}

// Inject and Extract carry a request trace through the desktop document to the worker.
func Inject(ctx context.Context) string {
	c := propagation.MapCarrier{}
	otel.GetTextMapPropagator().Inject(ctx, c)
	return c["traceparent"]
}
func Extract(ctx context.Context, traceparent string) context.Context {
	if traceparent == "" {
		return ctx
	}
	return otel.GetTextMapPropagator().Extract(ctx, propagation.MapCarrier{"traceparent": traceparent})
}

// LogHandler adds trace_id to slog records logged with a context.
type LogHandler struct{ slog.Handler }

func (h LogHandler) Handle(ctx context.Context, r slog.Record) error {
	if sc := trace.SpanContextFromContext(ctx); sc.IsValid() {
		r.AddAttrs(slog.String("trace_id", sc.TraceID().String()))
	}
	return h.Handler.Handle(ctx, r)
}
func (h LogHandler) WithAttrs(a []slog.Attr) slog.Handler { return LogHandler{h.Handler.WithAttrs(a)} }
func (h LogHandler) WithGroup(n string) slog.Handler      { return LogHandler{h.Handler.WithGroup(n)} }
