package cloud_test

import (
	"context"
	"devoops/vdi/internal/cloud"
	"io"
	"net"
	"testing"
	"time"
)

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
