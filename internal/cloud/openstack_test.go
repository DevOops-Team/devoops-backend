package cloud_test

import (
	"context"
	"devoops/vdi/internal/cloud"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	"go.opentelemetry.io/otel/trace"
	"io"
	"net"
	"testing"
	"time"
)

func TestRDPRequiresNegotiation(t *testing.T) {
	recorder := tracetest.NewSpanRecorder()
	provider := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(recorder))
	previous := otel.GetTracerProvider()
	otel.SetTracerProvider(provider)
	defer otel.SetTracerProvider(previous)
	defer provider.Shutdown(context.Background())
	listener, e := net.Listen("tcp", "127.0.0.1:3389")
	if e != nil {
		t.Skip("RDP test port in use")
	}
	defer listener.Close()
	responses := make(chan []byte, 3)
	responses <- []byte{3, 0, 0, 19, 14, 0xd0, 0, 0, 0, 0, 0, 2, 0, 8, 0, 1, 0, 0, 0}
	responses <- []byte{3, 0, 0, 19, 14, 0xd0, 0, 0, 0, 0, 0, 3, 0, 8, 0, 1, 0, 0, 0}
	responses <- []byte("HTTP/1.1")
	go func() {
		for range 3 {
			c, e := listener.Accept()
			if e != nil {
				return
			}
			_ = c.SetDeadline(time.Now().Add(time.Second))
			request := make([]byte, 19)
			_, _ = io.ReadFull(c, request)
			_, _ = c.Write(<-responses)
			_ = c.Close()
		}
	}()
	c := &cloud.OpenStack{Config: cloud.Config{Timeout: time.Second}}
	if !c.Ready(context.Background(), "127.0.0.1") {
		t.Fatal("valid RDP rejected")
	}
	if c.Ready(context.Background(), "127.0.0.1") {
		t.Fatal("RDP negotiation failure accepted")
	}
	if c.Ready(context.Background(), "127.0.0.1") {
		t.Fatal("non-RDP accepted")
	}

	spans := recorder.Ended()
	if len(spans) != 3 {
		t.Fatalf("got %d probe spans, want 3", len(spans))
	}
	for i, reason := range []string{"", "negotiation", "header"} {
		got := map[attribute.Key]attribute.Value{}
		for _, a := range spans[i].Attributes() {
			got[a.Key] = a.Value
		}
		if got["rdp.ready"].Type() != attribute.BOOL || got["rdp.ready"].AsBool() != (reason == "") || got["rdp.not_ready_reason"].AsString() != reason {
			t.Fatalf("probe %d: unexpected attributes %v", i, got)
		}
		if reason == "" {
			if _, exists := got["rdp.not_ready_reason"]; exists {
				t.Fatal("successful probe has failure reason")
			}
		}
		if spans[i].Status().Code != codes.Unset {
			t.Fatalf("probe %d marked as error", i)
		}
	}
}

func TestRDPReadySpan(t *testing.T) {
	recorder := tracetest.NewSpanRecorder()
	provider := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(recorder))
	previous := otel.GetTracerProvider()
	otel.SetTracerProvider(provider)
	defer otel.SetTracerProvider(previous)
	defer provider.Shutdown(context.Background())
	ctx, parent := provider.Tracer("test").Start(context.Background(), "parent")
	defer parent.End()
	c := &cloud.OpenStack{Config: cloud.Config{Timeout: 200 * time.Millisecond}}
	if c.Ready(ctx, "not-an-ip") {
		t.Fatal("invalid IP accepted")
	}
	spans := recorder.Ended()
	if len(spans) != 1 || spans[0].Name() != "rdp.ready" || spans[0].SpanKind() != trace.SpanKindClient {
		t.Fatalf("unexpected spans: %v", spans)
	}
	span := spans[0]
	if span.Parent().SpanID() != parent.SpanContext().SpanID() {
		t.Fatal("probe span lost parent")
	}
	if span.Status().Code != codes.Unset {
		t.Fatalf("not ready marked as error: %v", span.Status())
	}
	got := map[attribute.Key]attribute.Value{}
	for _, a := range span.Attributes() {
		got[a.Key] = a.Value
	}
	if got["rdp.ready"].Type() != attribute.BOOL || got["rdp.ready"].AsBool() || got["rdp.not_ready_reason"].AsString() != "invalid_ip" || got["server.port"].AsInt64() != 3389 || got["server.address"].AsString() != "not-an-ip" {
		t.Fatalf("unexpected attributes: %v", got)
	}
}
