package cloud_test

import (
	"devoops/vdi/internal/cloud"
	"testing"
)

func TestFlavorNameByOSDistro(t *testing.T) {
	for _, c := range []struct {
		properties map[string]any
		want       string
	}{
		{map[string]any{"os_distro": "windows"}, "m1.windows"},
		{map[string]any{"os_distro": "Windows"}, "m1.windows"},
		{map[string]any{"os_distro": "ubuntu"}, "m1.micro"},
		{map[string]any{}, "m1.micro"},
		{nil, "m1.micro"},
	} {
		if got := cloud.FlavorName(c.properties); got != c.want {
			t.Fatalf("%v: got %s want %s", c.properties, got, c.want)
		}
	}
}
