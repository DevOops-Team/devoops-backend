package cloud_test

import (
	"context"
	"devoops/vdi/internal/cloud"
	"devoops/vdi/internal/vdi"
	"encoding/json"
	"github.com/gophercloud/gophercloud/v2"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	"go.opentelemetry.io/otel/trace"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestCreateHomeVolume(t *testing.T) {
	for _, gb := range []int{0, 5} {
		var body map[string]any
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			_ = json.NewDecoder(r.Body).Decode(&body)
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusAccepted)
			_, _ = w.Write([]byte(`{"server":{"id":"vm-1"}}`))
		}))
		c := &cloud.OpenStack{Compute: &gophercloud.ServiceClient{ProviderClient: &gophercloud.ProviderClient{}, Endpoint: srv.URL + "/"},
			Config: cloud.Config{Network: "network", SecurityGroups: []string{"rdp"}, ServiceID: "test-vdi", Timeout: time.Second, HomeVolumeGB: gb, WindowsBootVolume: true}}
		d := vdi.Desktop{ImageID: "image-ubuntu", FlavorID: "flavor-micro"}
		d.OS.Type = "UBUNTU"
		d.ID, d.Name = 1, "desktop-1"
		id, e := c.Create(context.Background(), d)
		srv.Close()
		if e != nil || id != "vm-1" {
			t.Fatal(gb, id, e)
		}
		server, _ := body["server"].(map[string]any)
		if server["imageRef"] != "image-ubuntu" {
			t.Fatal(server)
		}
		if gb == 0 {
			if server["block_device_mapping_v2"] != nil {
				t.Fatal("home volume attached while disabled", server)
			}
			continue
		}
		bdm, _ := server["block_device_mapping_v2"].([]any)
		if len(bdm) != 2 {
			t.Fatal(server)
		}
		root, _ := bdm[0].(map[string]any)
		if root["source_type"] != "image" || root["destination_type"] != "local" || root["uuid"] != "image-ubuntu" || root["boot_index"] != float64(0) || root["delete_on_termination"] != true {
			t.Fatal(root)
		}
		b, _ := bdm[1].(map[string]any)
		if b["source_type"] != "blank" || b["destination_type"] != "volume" || b["volume_size"] != float64(gb) || b["boot_index"] != float64(-1) || b["delete_on_termination"] != true {
			t.Fatal(b)
		}
	}
}

func TestCreateWindowsBootVolume(t *testing.T) {
	for _, on := range []bool{false, true} {
		var body map[string]any
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			_ = json.NewDecoder(r.Body).Decode(&body)
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusAccepted)
			_, _ = w.Write([]byte(`{"server":{"id":"vm-1"}}`))
		}))
		c := &cloud.OpenStack{Compute: &gophercloud.ServiceClient{ProviderClient: &gophercloud.ProviderClient{}, Endpoint: srv.URL + "/"},
			Config: cloud.Config{Network: "network", SecurityGroups: []string{"rdp"}, ServiceID: "test-vdi", Timeout: time.Second, HomeVolumeGB: 5, WindowsBootVolume: on}}
		d := vdi.Desktop{ImageID: "image-windows", FlavorID: "flavor-windows"}
		d.ID, d.Name, d.StorageGB, d.OS.Type = 1, "desktop-1", 32, "WINDOWS"
		id, e := c.Create(context.Background(), d)
		srv.Close()
		if e != nil || id != "vm-1" {
			t.Fatal(on, id, e)
		}
		server, _ := body["server"].(map[string]any)
		bdm, _ := server["block_device_mapping_v2"].([]any)
		if !on {
			// disabled: same as Linux (image root disk + blank /home volume)
			if server["imageRef"] != "image-windows" || len(bdm) != 2 {
				t.Fatal(server)
			}
			continue
		}
		if server["imageRef"] != "" || server["flavorRef"] != "flavor-windows" || len(bdm) != 1 {
			t.Fatal(server)
		}
		root, _ := bdm[0].(map[string]any)
		if root["source_type"] != "image" || root["destination_type"] != "volume" || root["uuid"] != "image-windows" || root["volume_size"] != float64(32) || root["boot_index"] != float64(0) || root["delete_on_termination"] != true {
			t.Fatal(root)
		}
	}
}

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
