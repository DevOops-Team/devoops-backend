// Package testinfra provides an HTTP substitute for Keystone, Nova and Glance.
package testinfra

import (
	"context"
	"devoops/vdi/internal/cloud"
	"devoops/vdi/internal/vdi"
	"encoding/json"
	"fmt"
	"github.com/gophercloud/gophercloud/v2"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"time"
)

type OpenStack struct {
	CreateStarted  chan struct{}
	ContinueCreate chan struct{}
	FailGet        bool

	Server                                                   *httptest.Server
	Mu                                                       sync.Mutex
	VMs                                                      map[string]map[string]any
	Creates, Deletes, Authentications                        int
	RAM, Disk, MinRAM, MinDisk                               int
	VirtualSize                                              int64
	ImageStatus, VMStatus                                    string
	ReadyRDP, FailDelete, LoseCreate, RejectCreate, FailList bool
	RejectToken                                              bool
	LastCreate                                               map[string]any
}

func New() *OpenStack {
	s := &OpenStack{VMs: map[string]map[string]any{}, RAM: 1024, Disk: 10, VirtualSize: 10 << 30, ImageStatus: "active", VMStatus: "ACTIVE"}
	s.Server = httptest.NewServer(http.HandlerFunc(s.serve))
	return s
}
func (s *OpenStack) Set(fn func(*OpenStack)) { s.Mu.Lock(); defer s.Mu.Unlock(); fn(s) }
func (s *OpenStack) Counts() (int, int) {
	s.Mu.Lock()
	defer s.Mu.Unlock()
	return s.Creates, s.Deletes
}
func (s *OpenStack) serve(w http.ResponseWriter, r *http.Request) {
	s.Mu.Lock()
	defer s.Mu.Unlock()
	w.Header().Set("Content-Type", "application/json")
	write := func(status int, v any) { w.WriteHeader(status); _ = json.NewEncoder(w).Encode(v) }
	if r.URL.Path == "/v3/auth/tokens" {
		s.Authentications++
		token := fmt.Sprintf("token-%d", s.Authentications)
		w.Header().Set("X-Subject-Token", token)
		write(201, map[string]any{"token": map[string]any{"expires_at": time.Now().Add(time.Hour).UTC().Format(time.RFC3339), "catalog": []any{map[string]any{"type": "compute", "name": "nova", "endpoints": []any{map[string]any{"interface": "public", "region": "RegionOne", "url": s.Server.URL + "/compute/"}}}, map[string]any{"type": "image", "name": "glance", "endpoints": []any{map[string]any{"interface": "public", "region": "RegionOne", "url": s.Server.URL + "/image/"}}}}}})
		return
	}
	if s.RejectToken {
		s.RejectToken = false
		write(401, map[string]any{"error": "expired"})
		return
	}
	switch {
	case r.URL.Path == "/compute/" || r.URL.Path == "/image/":
		write(200, map[string]any{"versions": []any{map[string]any{"id": "v2.0", "status": "CURRENT", "links": []any{map[string]any{"rel": "self", "href": s.Server.URL + r.URL.Path}}}}})
	case r.URL.Path == "/compute/flavors/detail":
		write(200, map[string]any{"flavors": []any{map[string]any{"id": "flavor-micro", "name": "m1.micro", "vcpus": 1, "ram": s.RAM, "disk": s.Disk}}})
	case strings.HasPrefix(r.URL.Path, "/image/v2/images/"):
		if strings.HasSuffix(r.URL.Path, "missing") {
			write(404, map[string]any{})
			return
		}
		write(200, map[string]any{"id": "image-ubuntu", "name": "Ubuntu", "status": s.ImageStatus, "min_ram": s.MinRAM, "min_disk": s.MinDisk, "virtual_size": s.VirtualSize})
	case r.URL.Path == "/compute/servers" && r.Method == "POST":
		if s.CreateStarted != nil {
			close(s.CreateStarted)
			<-s.ContinueCreate
		}
		if s.RejectCreate {
			write(400, map[string]any{"error": "invalid"})
			return
		}
		var body map[string]any
		if json.NewDecoder(r.Body).Decode(&body) != nil {
			write(400, map[string]any{})
			return
		}
		server := body["server"].(map[string]any)
		s.LastCreate = server
		s.Creates++
		id := "vm-" + strconv.Itoa(s.Creates)
		vm := map[string]any{"id": id, "name": server["name"], "status": s.VMStatus, "metadata": server["metadata"], "addresses": map[string]any{"private": []any{map[string]any{"addr": "10.0.0.5", "version": 4, "OS-EXT-IPS:type": "fixed"}}}, "OS-EXT-SRV-ATTR:hypervisor_hostname": "compute-1"}
		s.VMs[id] = vm
		if s.LoseCreate {
			h := w.(http.Hijacker)
			c, _, _ := h.Hijack()
			_ = c.Close()
			return
		}
		write(202, map[string]any{"server": map[string]any{"id": id}})
	case r.URL.Path == "/compute/servers/detail":
		if s.FailList {
			write(503, map[string]any{})
			return
		}
		list := []any{}
		for _, v := range s.VMs {
			list = append(list, v)
		}
		write(200, map[string]any{"servers": list})
	case strings.HasPrefix(r.URL.Path, "/compute/servers/"):
		if r.Method == "GET" && s.FailGet {
			write(503, map[string]any{})
			return
		}
		id := strings.TrimPrefix(r.URL.Path, "/compute/servers/")
		vm, ok := s.VMs[id]
		if !ok {
			write(404, map[string]any{})
			return
		}
		if r.Method == "DELETE" {
			s.Deletes++
			if s.FailDelete {
				write(503, map[string]any{})
				return
			}
			delete(s.VMs, id)
			w.WriteHeader(204)
			return
		}
		write(200, map[string]any{"server": vm})
	default:
		write(404, map[string]any{"path": r.URL.Path})
	}
}

type Adapter struct {
	*cloud.OpenStack
	Fixture *OpenStack
}

func (a *Adapter) Ready(ctx context.Context, ip string) bool {
	a.Fixture.Mu.Lock()
	defer a.Fixture.Mu.Unlock()
	return a.Fixture.ReadyRDP && ip != ""
}
func (s *OpenStack) Client() (*Adapter, error) {
	o, e := cloud.New(context.Background(), cloud.Config{Auth: gophercloud.AuthOptions{IdentityEndpoint: s.Server.URL + "/v3", Username: "service", Password: "secret", DomainName: "Default", TenantID: "project"}, Region: "RegionOne", Network: "network", SecurityGroups: []string{"rdp"}, ServiceID: "test-vdi", Timeout: time.Second})
	if e != nil {
		return nil, e
	}
	return &Adapter{o, s}, nil
}

var _ vdi.Cloud = (*Adapter)(nil)
