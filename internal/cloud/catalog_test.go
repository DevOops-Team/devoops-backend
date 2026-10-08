package cloud

import (
	"github.com/gophercloud/gophercloud/v2/openstack/image/v2/images"
	"testing"
	"time"
)

func TestImageCatalog(t *testing.T) {
	old := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	newer := old.Add(time.Hour)
	image := func(id, distro, version string, created time.Time) images.Image {
		return images.Image{ID: id, Name: id, Status: "active", CreatedAt: created, Properties: map[string]any{"os_distro": distro, "os_version": version}}
	}
	invalidNewest := image("ubuntu-invalid", "ubuntu", "", newer.Add(time.Hour))
	inactive := image("windows-inactive", "windows", "11", newer)
	inactive.Status = "queued"
	list := []images.Image{
		image("ubuntu-old", "ubuntu", "22.04", old),
		image("ubuntu-z", "Ubuntu", "24.04", newer),
		image("cirros", "cirros", "0.6", newer),
		invalidNewest, inactive,
		image("ubuntu-a", " ubuntu ", " 24.04 ", newer),
		image("debian", "debian", "12", old),
	}
	for _, reverse := range []bool{false, true} {
		if reverse {
			for i, j := 0, len(list)-1; i < j; i, j = i+1, j-1 {
				list[i], list[j] = list[j], list[i]
			}
		}
		catalog := imageCatalog(list)
		if len(catalog) != 2 || catalog[0].ImageID != "ubuntu-a" || catalog[0].Type != "UBUNTU" || catalog[0].Version != "24.04" || catalog[1].Type != "DEBIAN" {
			t.Fatalf("unexpected catalog: %+v", catalog)
		}
	}
}
