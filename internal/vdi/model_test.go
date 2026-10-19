package vdi

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestDesktopResponseConnectionAndPrivacy(t *testing.T) {
	for _, status := range []string{"CREATING", "RUNNING", "FAILED", "DELETING", ""} {
		for _, connection := range []string{"PENDING", "READY", "FAILED", ""} {
			for _, actor := range []int64{7, 8} {
				d := Desktop{DesktopResponse: DesktopResponse{ID: 11, UserID: 7, Name: "vm", Status: status, ConnectionState: connection, CanConnect: true, OS: OS{ID: 1, Name: "Ubuntu", ImageID: "private-os-image"}}, VMID: "private-vm", ImageID: "private-image", FlavorID: "private-flavor", Lease: "private-lease"}
				r := d.Response(actor)
				want := actor == 7 && status == "RUNNING" && connection == "READY"
				if r.CanConnect != want {
					t.Fatalf("actor=%d status=%s connection=%s: %v", actor, status, connection, r.CanConnect)
				}
				if r.OS.ImageID != "" || d.OS.ImageID != "private-os-image" {
					t.Fatal("response must redact without mutating source")
				}
				if r.ID != 11 || r.Name != "vm" || r.OS.Name != "Ubuntu" {
					t.Fatal("public fields lost")
				}
				data, err := json.Marshal(r)
				if err != nil {
					t.Fatal(err)
				}
				if strings.Contains(string(data), "private-") || strings.Contains(string(data), "imageId") {
					t.Fatalf("internal data leaked: %s", data)
				}
			}
		}
	}
	// A persisted false value must also be recomputed when connection becomes ready.
	d := Desktop{DesktopResponse: DesktopResponse{UserID: 7, Status: "RUNNING", ConnectionState: "READY"}}
	if !d.Response(7).CanConnect {
		t.Fatal("stale false flag was not recomputed")
	}
}
