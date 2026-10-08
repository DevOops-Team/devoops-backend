package cloud

import (
	"context"
	"devoops/vdi/internal/vdi"
	"fmt"
	"github.com/gophercloud/gophercloud/v2"
	"github.com/gophercloud/gophercloud/v2/openstack"
	"github.com/gophercloud/gophercloud/v2/openstack/compute/v2/flavors"
	"github.com/gophercloud/gophercloud/v2/openstack/compute/v2/servers"
	"github.com/gophercloud/gophercloud/v2/openstack/image/v2/images"
	"io"
	"log/slog"
	"net"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"
)

type Config struct {
	Auth                       gophercloud.AuthOptions
	Region, Network, ServiceID string
	SecurityGroups             []string
	Timeout                    time.Duration
}
type OpenStack struct {
	Compute, Image *gophercloud.ServiceClient
	Config         Config
}

func New(ctx context.Context, cfg Config) (*OpenStack, error) {
	if cfg.Timeout <= 0 || cfg.Network == "" || cfg.ServiceID == "" || len(cfg.SecurityGroups) == 0 {
		return nil, fmt.Errorf("OpenStack network, service ID, security groups and timeout required")
	}
	cfg.Auth.AllowReauth = true
	provider, e := openstack.NewClient(cfg.Auth.IdentityEndpoint)
	if e != nil {
		return nil, fmt.Errorf("invalid OpenStack identity endpoint")
	}
	provider.HTTPClient = http.Client{Timeout: cfg.Timeout}
	if e = openstack.Authenticate(ctx, provider, cfg.Auth); e != nil {
		return nil, fmt.Errorf("OpenStack authentication failed")
	}
	eo := gophercloud.EndpointOpts{Region: cfg.Region}
	compute, e := openstack.NewComputeV2(provider, eo)
	if e != nil {
		return nil, fmt.Errorf("Nova endpoint unavailable")
	}
	image, e := openstack.NewImageV2(provider, eo)
	if e != nil {
		return nil, fmt.Errorf("Glance endpoint unavailable")
	}
	return &OpenStack{compute, image, cfg}, nil
}

func (o *OpenStack) ListImages(ctx context.Context) ([]vdi.OS, error) {
	ctx, cancel := context.WithTimeout(ctx, o.Config.Timeout)
	defer cancel()
	p, e := images.List(o.Image, images.ListOpts{Status: "active"}).AllPages(ctx)
	if e != nil {
		return nil, fmt.Errorf("Glance image listing failed")
	}
	list, e := images.ExtractImages(p)
	if e != nil {
		return nil, fmt.Errorf("invalid Glance image listing")
	}
	catalog := imageCatalog(list)
	slog.Info("Glance images fetched for initial OS catalog", "images", len(list), "registered", len(catalog))
	return catalog, nil
}

func imageCatalog(list []images.Image) []vdi.OS {
	// The existing catalog permits one image per OS type. Prefer the newest
	// eligible image, with a stable ID tie-break independent of API page order.
	sort.Slice(list, func(i, j int) bool {
		if list[i].CreatedAt.Equal(list[j].CreatedAt) {
			return list[i].ID < list[j].ID
		}
		return list[i].CreatedAt.After(list[j].CreatedAt)
	})
	catalog := []vdi.OS{}
	seen := map[string]bool{}
	for _, image := range list {
		distro, _ := image.Properties["os_distro"].(string)
		version, _ := image.Properties["os_version"].(string)
		typ := strings.ToUpper(strings.TrimSpace(distro))
		version = strings.TrimSpace(version)
		name := strings.TrimSpace(image.Name)
		if image.Status != "active" || image.ID == "" || name == "" || version == "" || seen[typ] {
			continue
		}
		switch typ {
		case "UBUNTU", "WINDOWS", "ROCKY", "DEBIAN":
			catalog = append(catalog, vdi.OS{Name: name, Type: typ, Version: version, ImageID: image.ID})
			seen[typ] = true
		}
	}
	return catalog
}
func (o *OpenStack) Spec(ctx context.Context, imageID string) (vdi.Spec, error) {
	ctx, cancel := context.WithTimeout(ctx, o.Config.Timeout)
	defer cancel()
	p, e := flavors.ListDetail(o.Compute, flavors.ListOpts{}).AllPages(ctx)
	if e != nil {
		return vdi.Spec{}, vdi.ErrCloudUnavailable
	}
	fs, e := flavors.ExtractFlavors(p)
	if e != nil {
		return vdi.Spec{}, vdi.ErrCloudUnavailable
	}
	var f *flavors.Flavor
	for i := range fs {
		if fs[i].Name == "m1.micro" {
			if f != nil {
				return vdi.Spec{}, vdi.ErrSpecUnavailable
			}
			f = &fs[i]
		}
	}
	if f == nil || f.VCPUs <= 0 || f.RAM <= 0 || f.RAM%1024 != 0 || f.Disk < 0 {
		return vdi.Spec{}, vdi.ErrSpecUnavailable
	}
	im, e := images.Get(ctx, o.Image, imageID).Extract()
	if e != nil {
		if gophercloud.ResponseCodeIs(e, 404) {
			return vdi.Spec{}, vdi.ErrOSUnavailable
		}
		return vdi.Spec{}, vdi.ErrCloudUnavailable
	}
	if im.Status != "active" || im.MinRAMMegabytes > f.RAM {
		return vdi.Spec{}, vdi.ErrOSUnavailable
	}
	disk := f.Disk
	if disk == 0 {
		const gb int64 = 1024 * 1024 * 1024
		if im.VirtualSize <= 0 || im.VirtualSize%gb != 0 {
			return vdi.Spec{}, vdi.ErrSpecUnavailable
		}
		disk = int(im.VirtualSize / gb)
	}
	if disk <= 0 {
		return vdi.Spec{}, vdi.ErrSpecUnavailable
	}
	if disk < im.MinDiskGigabytes {
		return vdi.Spec{}, vdi.ErrOSUnavailable
	}
	return vdi.Spec{FlavorID: f.ID, CPUCores: f.VCPUs, MemoryGB: f.RAM / 1024, StorageGB: disk}, nil
}
func (o *OpenStack) Create(ctx context.Context, d vdi.Desktop) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, o.Config.Timeout)
	defer cancel()
	opts := servers.CreateOpts{Name: d.Name, ImageRef: d.ImageID, FlavorRef: d.FlavorID, Networks: []servers.Network{{UUID: o.Config.Network}}, SecurityGroups: o.Config.SecurityGroups, Metadata: map[string]string{"vdi_service": o.Config.ServiceID, "vdi_desktop_id": strconv.FormatInt(d.ID, 10)}}
	vm, e := servers.Create(ctx, o.Compute, opts, nil).Extract()
	if e != nil {
		for _, code := range []int{400, 401, 403, 404, 409, 413, 422, 429} {
			if gophercloud.ResponseCodeIs(e, code) {
				return "", vdi.ErrCreateRejected
			}
		}
		return "", vdi.ErrCloudUnavailable
	}
	if vm.ID == "" {
		return "", vdi.ErrCloudUnavailable
	}
	return vm.ID, nil
}
func (o *OpenStack) Find(ctx context.Context, id int64) ([]vdi.VM, error) {
	ctx, cancel := context.WithTimeout(ctx, o.Config.Timeout)
	defer cancel()
	pages, e := servers.List(o.Compute, servers.ListOpts{}).AllPages(ctx)
	if e != nil {
		return nil, vdi.ErrCloudUnavailable
	}
	list, e := servers.ExtractServers(pages)
	if e != nil {
		return nil, vdi.ErrCloudUnavailable
	}
	out := []vdi.VM{}
	for _, s := range list {
		if s.Metadata["vdi_service"] == o.Config.ServiceID && s.Metadata["vdi_desktop_id"] == strconv.FormatInt(id, 10) {
			out = append(out, convert(s))
		}
	}
	return out, nil
}
func convert(s servers.Server) vdi.VM {
	vm := vdi.VM{ID: s.ID, Status: s.Status}
	for _, addresses := range s.Addresses {
		a, ok := addresses.([]any)
		if !ok {
			continue
		}
		for _, v := range a {
			m, ok := v.(map[string]any)
			if !ok {
				continue
			}
			ip, _ := m["addr"].(string)
			typ, _ := m["OS-EXT-IPS:type"].(string)
			if net.ParseIP(ip) != nil && (vm.IP == "" || typ == "fixed") {
				vm.IP = ip
			}
			if typ == "fixed" && vm.IP != "" {
				return vm
			}
		}
	}
	return vm
}
func (o *OpenStack) Get(ctx context.Context, id string) (vdi.VM, error) {
	ctx, cancel := context.WithTimeout(ctx, o.Config.Timeout)
	defer cancel()
	r, e := servers.Get(ctx, o.Compute, id).Extract()
	if e != nil {
		if gophercloud.ResponseCodeIs(e, 404) {
			return vdi.VM{}, vdi.ErrVMNotFound
		}
		return vdi.VM{}, vdi.ErrCloudUnavailable
	}
	v := convert(*r)
	v.Node = r.HypervisorHostname
	return v, nil
}
func (o *OpenStack) Delete(ctx context.Context, id string) error {
	ctx, cancel := context.WithTimeout(ctx, o.Config.Timeout)
	defer cancel()
	e := servers.Delete(ctx, o.Compute, id).ExtractErr()
	if e != nil && !gophercloud.ResponseCodeIs(e, 404) {
		return vdi.ErrCloudUnavailable
	}
	return nil
}
func (o *OpenStack) Ready(ctx context.Context, ip string) bool {
	if net.ParseIP(ip) == nil {
		return false
	}
	ctx, cancel := context.WithTimeout(ctx, o.Config.Timeout)
	defer cancel()
	// RDP negotiation, rather than a bare TCP accept, establishes protocol readiness.
	c, e := (&net.Dialer{}).DialContext(ctx, "tcp", net.JoinHostPort(ip, "3389"))
	if e != nil {
		return false
	}
	defer c.Close()
	deadline, _ := ctx.Deadline()
	_ = c.SetDeadline(deadline)
	request := []byte{3, 0, 0, 19, 14, 0xe0, 0, 0, 0, 0, 0, 1, 0, 8, 0, 3, 0, 0, 0}
	if _, e = c.Write(request); e != nil {
		return false
	}
	var header [4]byte
	if _, e = io.ReadFull(c, header[:]); e != nil || header[0] != 3 {
		return false
	}
	n := int(header[2])<<8 | int(header[3])
	if n < 11 || n > 4096 {
		return false
	}
	body := make([]byte, n-4)
	if _, e = io.ReadFull(c, body); e != nil {
		return false
	}
	return body[1] == 0xd0 && (len(body) < 15 || body[7] != 3)
}
