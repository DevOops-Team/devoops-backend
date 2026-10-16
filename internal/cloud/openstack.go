package cloud

import (
	"context"
	"devoops/vdi/internal/telemetry"
	"devoops/vdi/internal/vdi"
	"fmt"
	"github.com/gophercloud/gophercloud/v2"
	"github.com/gophercloud/gophercloud/v2/openstack"
	"github.com/gophercloud/gophercloud/v2/openstack/compute/v2/flavors"
	"github.com/gophercloud/gophercloud/v2/openstack/compute/v2/servers"
	"github.com/gophercloud/gophercloud/v2/openstack/image/v2/images"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"
	"io"
	"net"
	"net/http"
	"strconv"
	"strings"
	"time"
)

type Config struct {
	Auth                       gophercloud.AuthOptions
	Region, Network, ServiceID string
	SecurityGroups             []string
	Timeout                    time.Duration
	// HomeVolumeGB > 0 attaches a blank Cinder volume of that size as the second disk.
	// The desktop image mounts it on /home, so user data survives evacuation. 0 disables it.
	HomeVolumeGB int
	// WindowsBootVolume boots WINDOWS desktops from a Cinder volume made from the image (size = StorageGB).
	// Windows keeps user data in C:\Users, so the whole root disk survives evacuation instead of a /home volume.
	WindowsBootVolume bool
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
	provider.HTTPClient = http.Client{Timeout: cfg.Timeout, Transport: telemetry.Transport(nil)}
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
func FlavorName(properties map[string]any) string {
	if d, _ := properties["os_distro"].(string); strings.EqualFold(d, "windows") {
		return "m1.windows"
	}
	return "m1.micro"
}
func (o *OpenStack) Spec(ctx context.Context, imageID string) (vdi.Spec, error) {
	ctx, cancel := context.WithTimeout(ctx, o.Config.Timeout)
	defer cancel()
	im, e := images.Get(ctx, o.Image, imageID).Extract()
	if e != nil {
		if gophercloud.ResponseCodeIs(e, 404) {
			return vdi.Spec{}, vdi.ErrOSUnavailable
		}
		return vdi.Spec{}, vdi.ErrCloudUnavailable
	}
	name := FlavorName(im.Properties)
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
		if fs[i].Name == name {
			if f != nil {
				return vdi.Spec{}, vdi.ErrSpecUnavailable
			}
			f = &fs[i]
		}
	}
	if f == nil || f.VCPUs <= 0 || f.RAM <= 0 || f.RAM%1024 != 0 || f.Disk < 0 {
		return vdi.Spec{}, vdi.ErrSpecUnavailable
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
	switch {
	case o.Config.WindowsBootVolume && d.OS.Type == "WINDOWS":
		// Nova creates the root volume from the image before boot; it is deleted with the desktop.
		// Boot from volume takes the image from the mapping, so imageRef is sent empty.
		opts.ImageRef = ""
		opts.BlockDevice = []servers.BlockDevice{{SourceType: servers.SourceImage, DestinationType: servers.DestinationVolume, UUID: d.ImageID, VolumeSize: d.StorageGB, BootIndex: 0, DeleteOnTermination: true}}
	case o.Config.HomeVolumeGB > 0:
		// Nova creates the volume and attaches it before boot; it is deleted with the desktop.
		// With block_device_mapping_v2 Nova needs the image root disk listed as boot_index 0.
		opts.BlockDevice = []servers.BlockDevice{{SourceType: servers.SourceImage, DestinationType: servers.DestinationLocal, UUID: d.ImageID, BootIndex: 0, DeleteOnTermination: true}, {SourceType: servers.SourceBlank, DestinationType: servers.DestinationVolume, VolumeSize: o.Config.HomeVolumeGB, BootIndex: -1, DeleteOnTermination: true}}
	}
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

// Ready records the raw TCP RDP probe that HTTP instrumentation cannot observe.
func (o *OpenStack) Ready(ctx context.Context, ip string) bool {
	ctx, span := otel.Tracer("cloud").Start(ctx, "rdp.ready", trace.WithSpanKind(trace.SpanKindClient),
		trace.WithAttributes(attribute.String("server.address", ip), attribute.Int("server.port", 3389)))
	defer span.End()
	reason := o.rdpReady(ctx, ip)
	span.SetAttributes(attribute.Bool("rdp.ready", reason == ""))
	if reason != "" {
		// Not ready is normal while the VM boots, so the span status stays unset.
		span.SetAttributes(attribute.String("rdp.not_ready_reason", reason))
	}
	return reason == ""
}

// rdpReady returns an empty string on success, otherwise the failed probe step.
func (o *OpenStack) rdpReady(ctx context.Context, ip string) string {
	if net.ParseIP(ip) == nil {
		return "invalid_ip"
	}
	ctx, cancel := context.WithTimeout(ctx, o.Config.Timeout)
	defer cancel()
	// RDP negotiation, rather than a bare TCP accept, establishes protocol readiness.
	c, e := (&net.Dialer{}).DialContext(ctx, "tcp", net.JoinHostPort(ip, "3389"))
	if e != nil {
		return "dial"
	}
	defer c.Close()
	deadline, _ := ctx.Deadline()
	_ = c.SetDeadline(deadline)
	request := []byte{3, 0, 0, 19, 14, 0xe0, 0, 0, 0, 0, 0, 1, 0, 8, 0, 3, 0, 0, 0}
	if _, e = c.Write(request); e != nil {
		return "write"
	}
	var header [4]byte
	if _, e = io.ReadFull(c, header[:]); e != nil || header[0] != 3 {
		return "header"
	}
	n := int(header[2])<<8 | int(header[3])
	if n < 11 || n > 4096 {
		return "length"
	}
	body := make([]byte, n-4)
	if _, e = io.ReadFull(c, body); e != nil {
		return "body"
	}
	if body[1] == 0xd0 && (len(body) < 15 || body[7] != 3) {
		return ""
	}
	return "negotiation"
}
