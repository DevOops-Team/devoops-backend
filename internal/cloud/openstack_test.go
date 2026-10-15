package cloud_test

import (
	"context"
	"devoops/vdi/internal/cloud"
	"devoops/vdi/internal/vdi"
	"encoding/json"
	"github.com/gophercloud/gophercloud/v2"
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
			Config: cloud.Config{Network: "network", SecurityGroups: []string{"rdp"}, ServiceID: "test-vdi", Timeout: time.Second, HomeVolumeGB: gb}}
		d := vdi.Desktop{ImageID: "image-ubuntu", FlavorID: "flavor-micro"}
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
		if len(bdm) != 1 {
			t.Fatal(server)
		}
		b, _ := bdm[0].(map[string]any)
		if b["source_type"] != "blank" || b["destination_type"] != "volume" || b["volume_size"] != float64(gb) || b["boot_index"] != float64(-1) || b["delete_on_termination"] != true {
			t.Fatal(b)
		}
	}
}

func TestRDPRequiresNegotiation(t *testing.T) {
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
}
