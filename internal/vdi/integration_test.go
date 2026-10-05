package vdi_test

import (
	"bytes"
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/hmac"
	"crypto/sha256"
	"devoops/vdi/internal/guacamole"
	"devoops/vdi/internal/testinfra"
	"devoops/vdi/internal/vdi"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"github.com/google/uuid"
	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo/options"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"reflect"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"
)

type fixture struct {
	t       *testing.T
	app     *vdi.App
	store   *vdi.Store
	cloud   *testinfra.OpenStack
	server  *httptest.Server
	worker  *vdi.Worker
	now     time.Time
	admin   string
	osID    int64
	adminID int64
}

func setup(t *testing.T) *fixture {
	t.Helper()
	uri := os.Getenv("TEST_MONGO_URI")
	if uri == "" {
		t.Skip("TEST_MONGO_URI required; run make test-integration")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	s, e := vdi.OpenStore(ctx, uri, "vdi_test_"+strings.ReplaceAll(uuid.NewString(), "-", ""))
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() { _ = s.DB.Drop(context.Background()); _ = s.DB.Client().Disconnect(context.Background()) })
	images := []vdi.OS{{Name: "Ubuntu", Type: "UBUNTU", Version: "24.04", ImageID: "image-ubuntu"}, {Name: "Windows", Type: "WINDOWS", Version: "11", ImageID: "image-windows"}, {Name: "Rocky", Type: "ROCKY", Version: "9", ImageID: "image-rocky"}, {Name: "Debian", Type: "DEBIAN", Version: "12", ImageID: "image-debian"}}
	if e = s.Bootstrap(ctx, "admin", "admin@example.com", "secret", images); e != nil {
		t.Fatal(e)
	}
	cloud := testinfra.New()
	t.Cleanup(cloud.Server.Close)
	adapter, e := cloud.Client()
	if e != nil {
		t.Fatal(e)
	}
	g, e := guacamole.New("https://vdi.example.com/guacamole/", "00112233445566778899aabbccddeeff", map[string]guacamole.Credential{"UBUNTU": {Username: "rdp-user", Password: "rdp-secret"}}, 2*time.Minute)
	if e != nil {
		t.Fatal(e)
	}
	a := vdi.NewApp(s, adapter, g)
	f := &fixture{t: t, app: a, store: s, cloud: cloud, now: time.Now().UTC().Truncate(time.Millisecond)}
	a.Now = func() time.Time { return f.now }
	f.worker = &vdi.Worker{Store: s, Cloud: adapter, Now: a.Now, Interval: time.Millisecond, ReadyTimeout: time.Minute, LeaseDuration: time.Second}
	f.server = httptest.NewServer(a.Handler())
	t.Cleanup(f.server.Close)
	login := f.request("POST", "/api/auth/login", "", map[string]any{"email": "admin@example.com", "password": "secret"}, "", 200)
	f.admin = login["token"].(string)
	f.adminID = int64(login["user"].(map[string]any)["id"].(float64))
	var im vdi.OS
	if e = s.C("os").FindOne(ctx, bson.M{"type": "UBUNTU"}).Decode(&im); e != nil {
		t.Fatal(e)
	}
	f.osID = im.ID
	return f
}
func (f *fixture) raw(method, path, token string, body any, key string) (int, []byte) {
	var b io.Reader
	if body != nil {
		data, _ := json.Marshal(body)
		b = bytes.NewReader(data)
	}
	r, _ := http.NewRequest(method, f.server.URL+path, b)
	if token != "" {
		r.Header.Set("Authorization", "Bearer "+token)
	}
	if body != nil {
		r.Header.Set("Content-Type", "application/json")
	}
	if key != "" {
		r.Header.Set("Idempotency-Key", key)
	}
	resp, e := http.DefaultClient.Do(r)
	if e != nil {
		f.t.Error(e)
		return 0, nil
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, data
}
func (f *fixture) request(method, path, token string, body any, key string, status int) map[string]any {
	f.t.Helper()
	got, data := f.raw(method, path, token, body, key)
	if got != status {
		f.t.Fatalf("%s %s: got %d want %d: %s", method, path, got, status, data)
	}
	if status == 204 {
		if len(data) != 0 {
			f.t.Fatal("204 has body")
		}
		return nil
	}
	var v map[string]any
	if e := json.Unmarshal(data, &v); e != nil {
		f.t.Fatalf("object: %s", data)
	}
	return v
}
func (f *fixture) list(path, token string) []map[string]any {
	f.t.Helper()
	status, data := f.raw("GET", path, token, nil, "")
	if status != 200 {
		f.t.Fatalf("list %s: %d %s", path, status, data)
	}
	var out []map[string]any
	if e := json.Unmarshal(data, &out); e != nil || out == nil {
		f.t.Fatalf("expected array: %s", data)
	}
	return out
}
func (f *fixture) user(email, role string) (int64, string) {
	v := f.request("POST", "/api/admin/users", f.admin, map[string]any{"name": "User", "email": email, "password": "pass", "role": role}, "", 201)
	login := f.request("POST", "/api/auth/login", "", map[string]any{"email": email, "password": "pass"}, "", 200)
	return int64(v["id"].(float64)), login["token"].(string)
}
func (f *fixture) body() map[string]any {
	return map[string]any{"name": "개발용", "osId": f.osID, "cpuCores": 1, "memoryGb": 1, "storageGb": 10}
}
func (f *fixture) create(token string) int64 {
	v := f.request("POST", "/api/desktops", token, f.body(), uuid.NewString(), 202)
	return int64(v["id"].(float64))
}
func (f *fixture) tick(n int) {
	f.t.Helper()
	for range n {
		f.now = f.now.Add(2 * time.Millisecond)
		if e := f.worker.Tick(context.Background()); e != nil {
			f.t.Fatal(e)
		}
	}
}
func path(id int64) string { return fmt.Sprintf("/api/desktops/%d", id) }
func keys(t *testing.T, v map[string]any, want ...string) {
	t.Helper()
	got := []string{}
	for k := range v {
		got = append(got, k)
	}
	sort.Strings(got)
	sort.Strings(want)
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("keys got %v want %v", got, want)
	}
}
func TestAuthenticationAndLogout(t *testing.T) {
	f := setup(t)
	keys(t, f.request("GET", "/api/me", f.admin, nil, "", 200), "id", "name", "email", "role", "createdAt", "updatedAt")
	a := f.request("POST", "/api/auth/login", "", map[string]any{"email": "missing@example.com", "password": "wrong"}, "", 401)
	b := f.request("POST", "/api/auth/login", "", map[string]any{"email": "admin@example.com", "password": "wrong"}, "", 401)
	if !reflect.DeepEqual(a, b) {
		t.Fatal("credential errors differ")
	}
	keys(t, a, "code", "message", "details")
	f.request("POST", "/api/auth/login", "", map[string]any{"username": "admin"}, "", 400)
	login := f.request("POST", "/api/auth/login", "", map[string]any{"email": "admin@example.com", "password": "secret"}, "", 200)
	keys(t, login, "token", "expiresAt", "user")
	expires, _ := time.Parse(time.RFC3339, login["expiresAt"].(string))
	if !expires.Equal(f.now.Add(8 * time.Hour)) {
		t.Fatal("session TTL")
	}
	second := login["token"].(string)
	f.request("POST", "/api/auth/logout", f.admin, nil, "", 204)
	f.request("GET", "/api/me", f.admin, nil, "", 401)
	f.request("POST", "/api/auth/logout", f.admin, nil, "", 401)
	f.request("GET", "/api/me", second, nil, "", 200)
	events := f.list("/api/admin/events?action=LOGOUT", second)
	if len(events) != 1 || events[0]["desktopId"] != nil || events[0]["targetType"] != "USER" {
		t.Fatal(events)
	}
	if strings.Contains(fmt.Sprint(events), f.admin) {
		t.Fatal("token leaked")
	}
	f.now = expires
	f.request("GET", "/api/me", second, nil, "", 401)
	f.request("POST", "/api/auth/logout", second, nil, "", 401)
}
func TestBootstrapAndLogoutAtomicity(t *testing.T) {
	f := setup(t)
	var admin vdi.User
	_ = f.store.C("users").FindOne(context.Background(), bson.M{"id": f.adminID}).Decode(&admin)
	if e := f.store.Bootstrap(context.Background(), "changed", "admin@example.com", "different", nil); e != nil {
		t.Fatal(e)
	}
	f.request("POST", "/api/auth/login", "", map[string]any{"email": "admin@example.com", "password": "secret"}, "", 200)
	// Inject an event insertion failure using an actual MongoDB validator. Transaction must retain session.
	_, e := f.store.DB.RunCommand(context.Background(), bson.D{{Key: "collMod", Value: "events"}, {Key: "validator", Value: bson.M{"action": bson.M{"$ne": "LOGOUT"}}}}).Raw()
	if e != nil {
		t.Fatal(e)
	}
	f.request("POST", "/api/auth/logout", f.admin, nil, "", 500)
	f.request("GET", "/api/me", f.admin, nil, "", 200)
	n, _ := f.store.C("events").CountDocuments(context.Background(), bson.M{"action": "LOGOUT"})
	if n != 0 {
		t.Fatal("partial event")
	}
	var session bson.M
	if e = f.store.C("sessions").FindOne(context.Background(), bson.M{}).Decode(&session); e != nil {
		t.Fatal(e)
	}
	if _, ok := session["token"]; ok {
		t.Fatal("raw session token")
	}
}
func TestDesktopLifecycleAndGuacamole(t *testing.T) {
	f := setup(t)
	_, user := f.user("user@example.com", "USER")
	_, other := f.user("other@example.com", "USER")
	if len(f.list("/api/desktops", user)) != 0 {
		t.Fatal("not empty")
	}
	images := f.list("/api/images", user)
	if len(images) != 4 {
		t.Fatal(images)
	}
	keys(t, images[0], "id", "name", "type", "version")
	n := f.create(user)
	v := f.request("GET", path(n), user, nil, "", 200)
	keys(t, v, "id", "userId", "name", "osId", "cpuCores", "memoryGb", "storageGb", "os", "status", "nodeName", "connectionState", "canConnect", "failure", "createdAt", "updatedAt")
	if v["status"] != "CREATING" || v["canConnect"] != false || v["nodeName"] != nil || v["failure"] != nil {
		t.Fatal(v)
	}
	for _, tok := range []string{other, f.admin} {
		f.request("GET", path(n), tok, nil, "", 404)
		f.request("POST", path(n)+"/connect", tok, nil, "", 404)
		f.request("DELETE", path(n), tok, nil, "", 404)
	}
	f.request("GET", "/api/admin/summary", user, nil, "", 403)
	f.tick(1)
	v = f.request("GET", path(n), user, nil, "", 200)
	if v["status"] != "RUNNING" || v["connectionState"] != "PENDING" || v["canConnect"] != false {
		t.Fatal(v)
	}
	f.request("POST", path(n)+"/connect", user, nil, "", 409)
	f.cloud.Set(func(s *testinfra.OpenStack) { s.ReadyRDP = true })
	f.tick(3)
	v = f.request("GET", path(n), user, nil, "", 200)
	if v["connectionState"] != "READY" || v["canConnect"] != true {
		t.Fatal(v)
	}
	con := f.request("POST", path(n)+"/connect", user, nil, "", 200)
	keys(t, con, "url", "expiresAt")
	u, _ := url.Parse(con["url"].(string))
	enc, e := base64.StdEncoding.DecodeString(u.Query().Get("data"))
	if e != nil {
		t.Fatal(e)
	}
	key, _ := hex.DecodeString("00112233445566778899aabbccddeeff")
	block, _ := aes.NewCipher(key)
	plain := make([]byte, len(enc))
	cipher.NewCBCDecrypter(block, make([]byte, 16)).CryptBlocks(plain, enc)
	plain = plain[:len(plain)-int(plain[len(plain)-1])]
	mac := hmac.New(sha256.New, key)
	mac.Write(plain[32:])
	if !hmac.Equal(mac.Sum(nil), plain[:32]) {
		t.Fatal("signature")
	}
	var payload map[string]any
	_ = json.Unmarshal(plain[32:], &payload)
	connections := payload["connections"].(map[string]any)
	if len(connections) != 1 || int64(payload["expires"].(float64)) != f.now.Add(2*time.Minute).UnixMilli() {
		t.Fatal(payload)
	}
	// A stale READY snapshot must not authorize a failed readiness probe.
	f.cloud.Set(func(s *testinfra.OpenStack) { s.ReadyRDP = false })
	f.request("POST", path(n)+"/connect", user, nil, "", 409)
	users := f.list("/api/admin/users", f.admin)
	if len(users) != 3 {
		t.Fatal(users)
	}
	summary := f.request("GET", "/api/admin/summary", f.admin, nil, "", 200)
	keys(t, summary, "users", "admins", "desktops", "byStatus", "byOs", "byNode")
	if summary["byNode"].(map[string]any)["compute-1"] != float64(1) {
		t.Fatal(summary)
	}
	if summary["desktops"] != float64(1) {
		t.Fatal(summary)
	}
	d := f.request("DELETE", path(n), user, nil, "", 202)
	keys(t, d, "id", "status")
	f.request("DELETE", path(n), user, nil, "", 409)
	f.request("GET", path(n), user, nil, "", 200)
	f.tick(6)
	f.request("GET", path(n), user, nil, "", 404)
	if len(f.list("/api/desktops", user)) != 0 {
		t.Fatal("deleted listed")
	}
	f.cloud.Mu.Lock()
	last := f.cloud.LastCreate
	f.cloud.Mu.Unlock()
	if last["imageRef"] != "image-ubuntu" || last["flavorRef"] != "flavor-micro" || last["block_device_mapping_v2"] != nil {
		t.Fatal(last)
	}
}
func TestConcurrentQuotaAndIdempotency(t *testing.T) {
	f := setup(t)
	_, user := f.user("user@example.com", "USER")
	body := f.body()
	key := uuid.NewString()
	first := f.request("POST", "/api/desktops", user, body, key, 202)
	var wg sync.WaitGroup
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			status, data := f.raw("POST", "/api/desktops", user, body, key)
			if status != 202 {
				t.Errorf("replay %d %s", status, data)
			}
			var v map[string]any
			json.Unmarshal(data, &v)
			if !reflect.DeepEqual(first, v) {
				t.Error("response changed")
			}
		}()
	}
	wg.Wait()
	body["name"] = "different"
	f.request("POST", "/api/desktops", user, body, key, 409)
	accepted := 0
	var mu sync.Mutex
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			status, data := f.raw("POST", "/api/desktops", user, f.body(), uuid.NewString())
			if status == 202 {
				mu.Lock()
				accepted++
				mu.Unlock()
			} else if status != 409 {
				t.Errorf("quota %d %s", status, data)
			}
		}()
	}
	wg.Wait()
	if accepted != 1 {
		t.Fatalf("accepted %d", accepted)
	}
	f.tick(10)
	creates, _ := f.cloud.Counts()
	if creates != 2 {
		t.Fatal("VM count", creates)
	}
	target, other := f.user("other@example.com", "USER")
	f.request("POST", "/api/desktops", other, f.body(), key, 202)
	assign := f.body()
	assign["userId"] = target
	f.request("POST", "/api/admin/desktops", f.admin, assign, uuid.NewString(), 202)
	f.request("POST", "/api/admin/desktops", f.admin, assign, uuid.NewString(), 409)
	f.request("POST", "/api/desktops", user, f.body(), key, 202) // replay ignores quota
}
func TestAvailabilityValidation(t *testing.T) {
	f := setup(t)
	for _, body := range []map[string]any{{"name": "x"}, {"name": "x", "osId": f.osID, "cpuCores": 1, "memoryGb": 1, "storageGb": 10, "userId": 4}} {
		f.request("POST", "/api/desktops", f.admin, body, uuid.NewString(), 400)
	}
	f.request("POST", "/api/desktops", f.admin, f.body(), "", 400)
	b := f.body()
	b["cpuCores"] = 2
	f.request("POST", "/api/desktops", f.admin, b, uuid.NewString(), 400)
	f.cloud.Set(func(s *testinfra.OpenStack) { s.RAM = 512 })
	f.request("POST", "/api/desktops", f.admin, f.body(), uuid.NewString(), 409)
	if len(f.list("/api/images", f.admin)) != 0 {
		t.Fatal("incompatible images")
	}
	f.cloud.Set(func(s *testinfra.OpenStack) { s.RAM = 1024; s.Disk = 0; s.VirtualSize = 0 })
	f.request("POST", "/api/desktops", f.admin, f.body(), uuid.NewString(), 409)
	f.cloud.Set(func(s *testinfra.OpenStack) { s.VirtualSize = 10 << 30; s.MinDisk = 11 })
	f.request("POST", "/api/desktops", f.admin, f.body(), uuid.NewString(), 409)
	f.cloud.Set(func(s *testinfra.OpenStack) { s.MinDisk = 0; s.MinRAM = 2048 })
	f.request("POST", "/api/desktops", f.admin, f.body(), uuid.NewString(), 409)
	f.cloud.Set(func(s *testinfra.OpenStack) { s.MinRAM = 0; s.ImageStatus = "queued" })
	if len(f.list("/api/images", f.admin)) != 0 {
		t.Fatal("inactive")
	}
	f.cloud.Set(func(s *testinfra.OpenStack) { s.ImageStatus = "active"; s.RejectToken = true })
	if len(f.list("/api/images", f.admin)) != 4 {
		t.Fatal("reauth")
	}
}
func TestResponseLossRecoveryAndWorkerClaims(t *testing.T) {
	f := setup(t)
	f.cloud.Set(func(s *testinfra.OpenStack) { s.LoseCreate = true })
	n := f.create(f.admin)
	f.now = f.now.Add(time.Second)
	var wg sync.WaitGroup
	for range 5 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if e := f.worker.Tick(context.Background()); e != nil {
				t.Error(e)
			}
		}()
	}
	wg.Wait()
	f.tick(4)
	creates, _ := f.cloud.Counts()
	if creates != 1 {
		t.Fatal("duplicate", creates)
	}
	v := f.request("GET", path(n), f.admin, nil, "", 200)
	if v["status"] != "RUNNING" {
		t.Fatal(v)
	}
	// Durable ambiguous submission survives a new App/Worker instance without a POST.
	another := f.create(f.admin)
	_, e := f.store.C("desktops").UpdateOne(context.Background(), bson.M{"id": another}, bson.M{"$set": bson.M{"submitted": true, "phase": "submitted"}})
	if e != nil {
		t.Fatal(e)
	}
	clone := *f.worker
	f.worker = &clone
	f.tick(4)
	after, _ := f.cloud.Counts()
	if after != creates {
		t.Fatal("ambiguous replay submitted again")
	}
	f.now = f.now.Add(2 * time.Minute)
	f.tick(4)
	v = f.request("GET", path(another), f.admin, nil, "", 200)
	if v["status"] != "ERROR" || v["failure"] == nil {
		t.Fatal(v)
	}
}
func TestTimeoutCleanupAndDeletionRetries(t *testing.T) {
	f := setup(t)
	n := f.create(f.admin)
	f.tick(1)
	f.cloud.Set(func(s *testinfra.OpenStack) { s.FailDelete = true })
	f.now = f.now.Add(2 * time.Minute)
	f.tick(2)
	v := f.request("GET", path(n), f.admin, nil, "", 200)
	if v["status"] != "ERROR" {
		t.Fatal(v)
	}
	beforeCreate, beforeDelete := f.cloud.Counts()
	f.list("/api/desktops", f.admin)
	f.request("GET", path(n), f.admin, nil, "", 200)
	c, d := f.cloud.Counts()
	if c != beforeCreate || d != beforeDelete {
		t.Fatal("GET mutated cloud")
	}
	f.request("POST", "/api/desktops", f.admin, f.body(), uuid.NewString(), 409)
	f.cloud.Set(func(s *testinfra.OpenStack) { s.FailDelete = false })
	f.tick(5)
	f.create(f.admin) // reservation returned
	events := f.list("/api/admin/events?action=DESKTOP_CREATE_FAILED", f.admin)
	if len(events) != 1 {
		t.Fatal(events)
	}
	f.tick(4)
	list := f.list("/api/desktops", f.admin)
	var active int64
	for _, v := range list {
		if v["status"] != "ERROR" {
			active = int64(v["id"].(float64))
		}
	}
	f.cloud.Set(func(s *testinfra.OpenStack) { s.FailDelete = true })
	f.request("DELETE", path(active), f.admin, nil, "", 202)
	f.tick(3)
	v = f.request("GET", path(active), f.admin, nil, "", 200)
	if v["status"] != "ERROR" || v["failure"].(map[string]any)["code"] != "DESKTOP_DELETE_FAILED" {
		t.Fatal(v)
	}
	f.cloud.Set(func(s *testinfra.OpenStack) { s.FailDelete = false })
	f.tick(6)
	f.request("GET", path(active), f.admin, nil, "", 404)
}
func TestAdminMutationsAndEventFilters(t *testing.T) {
	f := setup(t)
	uid, token := f.user("user@example.com", "USER")
	n := f.create(token)
	assign := f.body()
	assign["userId"] = uid
	f.request("POST", "/api/admin/desktops", f.admin, assign, uuid.NewString(), 202)
	f.request("PATCH", fmt.Sprintf("/api/admin/users/%d", uid), f.admin, map[string]any{"name": "Changed", "email": "new@example.com"}, "", 200)
	f.request("GET", "/api/me", token, nil, "", 200)
	for _, b := range []map[string]any{{}, {"name": nil}, {"password": "x"}} {
		f.request("PATCH", fmt.Sprintf("/api/admin/users/%d", uid), f.admin, b, "", 400)
	}
	f.request("PATCH", fmt.Sprintf("/api/admin/users/%d", uid), f.admin, map[string]any{"role": "ADMIN"}, "", 200)
	f.request("GET", "/api/me", token, nil, "", 401)
	f.request("DELETE", fmt.Sprintf("/api/admin/users/%d", f.adminID), f.admin, nil, "", 403)
	f.tick(4)
	f.request("DELETE", fmt.Sprintf("/api/admin/desktops/%d", n), f.admin, nil, "", 202)
	deleted := f.request("DELETE", fmt.Sprintf("/api/admin/users/%d", uid), f.admin, nil, "", 200)
	keys(t, deleted, "userId", "reclaimedDesktopIds")
	if len(deleted["reclaimedDesktopIds"].([]any)) != 2 {
		t.Fatal(deleted)
	}
	if len(f.list(fmt.Sprintf("/api/admin/desktops?userId=%d", uid), f.admin)) != 2 {
		t.Fatal("deleted before completion")
	}
	f.tick(12)
	if len(f.list(fmt.Sprintf("/api/admin/desktops?userId=%d", uid), f.admin)) != 0 {
		t.Fatal("not reclaimed")
	}
	f.request("PATCH", fmt.Sprintf("/api/admin/users/%d", f.adminID), f.admin, map[string]any{"role": "USER"}, "", 403)
	for _, query := range []string{"limit=0", "limit=501", "actorId=-1", "desktopId=9007199254740992", "action=USER_CREATE", "targetType=VM"} {
		f.request("GET", "/api/admin/events?"+query, f.admin, nil, "", 400)
	}
	events := f.list(fmt.Sprintf("/api/admin/events?actorId=%d&desktopId=%d&action=DESKTOP_RELEASE&targetType=DESKTOP&limit=1", f.adminID, n), f.admin)
	if len(events) != 1 {
		t.Fatal(events)
	}
	keys(t, events[0], "id", "actorId", "desktopId", "action", "targetType", "detail", "createdAt")
}
func TestConcurrentLastAdminProtection(t *testing.T) {
	f := setup(t)
	id2, token2 := f.user("admin2@example.com", "ADMIN")
	var wg sync.WaitGroup
	statuses := make(chan int, 2)
	for _, p := range []struct {
		id    int64
		token string
	}{{f.adminID, f.admin}, {id2, token2}} {
		wg.Add(1)
		go func(p struct {
			id    int64
			token string
		}) {
			defer wg.Done()
			status, _ := f.raw("PATCH", fmt.Sprintf("/api/admin/users/%d", p.id), p.token, map[string]any{"role": "USER"}, "")
			statuses <- status
		}(p)
	}
	wg.Wait()
	close(statuses)
	success := 0
	for s := range statuses {
		if s == 200 {
			success++
		} else if s != 403 {
			t.Fatal(s)
		}
	}
	if success != 1 {
		t.Fatal("demotions", success)
	}
	count, e := f.store.C("users").CountDocuments(context.Background(), bson.M{"role": "ADMIN", "deleted": false, "internal": false})
	if e != nil || count != 1 {
		t.Fatal(count, e)
	}
}
func TestExpiredIdempotencyAndCommonErrors(t *testing.T) {
	f := setup(t)
	key := uuid.NewString()
	v := f.request("POST", "/api/desktops", f.admin, f.body(), key, 202)
	_, e := f.store.C("idempotency").UpdateOne(context.Background(), bson.M{"key": key}, bson.M{"$set": bson.M{"expiresAt": f.now.Add(-time.Second)}})
	if e != nil {
		t.Fatal(e)
	}
	second := f.request("POST", "/api/desktops", f.admin, f.body(), key, 202)
	if v["id"] == second["id"] {
		t.Fatal("expired replay")
	}
	for _, p := range []string{"/api/desktops/0", "/api/desktops/9007199254740992", "/api/admin/desktops?userId=x"} {
		f.request("GET", p, f.admin, nil, "", 400)
	}
	f.request("GET", "/api/no-such-path", f.admin, nil, "", 404)
	f.request("PUT", "/api/me", f.admin, nil, "", 405)
	var indexes []bson.M
	cur, e := f.store.C("idempotency").Indexes().List(context.Background(), options.ListIndexes())
	if e != nil {
		t.Fatal(e)
	}
	defer cur.Close(context.Background())
	if e = cur.All(context.Background(), &indexes); e != nil {
		t.Fatal(e)
	}
	if len(indexes) < 3 {
		t.Fatal(indexes)
	}
}

func TestKnownCreateRejectionAndDuplicateCleanup(t *testing.T) {
	t.Run("definitive rejection releases quota", func(t *testing.T) {
		f := setup(t)
		f.cloud.Set(func(s *testinfra.OpenStack) { s.RejectCreate = true })
		n := f.create(f.admin)
		f.tick(2)
		v := f.request("GET", path(n), f.admin, nil, "", 200)
		if v["status"] != "ERROR" || v["failure"].(map[string]any)["code"] != "OPENSTACK_CREATE_FAILED" {
			t.Fatal(v)
		}
		f.cloud.Set(func(s *testinfra.OpenStack) { s.RejectCreate = false })
		f.create(f.admin)
		f.create(f.admin)
		f.request("POST", "/api/desktops", f.admin, f.body(), uuid.NewString(), 409)
		if len(f.list("/api/admin/events?action=DESKTOP_CREATE_FAILED", f.admin)) != 1 {
			t.Fatal("missing failure event")
		}
	})
	t.Run("identified duplicates clean up after restart", func(t *testing.T) {
		f := setup(t)
		n := f.create(f.admin)
		_, e := f.store.C("desktops").UpdateOne(context.Background(), bson.M{"id": n}, bson.M{"$set": bson.M{"submitted": true, "phase": "submitted"}})
		if e != nil {
			t.Fatal(e)
		}
		f.cloud.Set(func(s *testinfra.OpenStack) {
			for _, id := range []string{"duplicate-a", "duplicate-b"} {
				s.VMs[id] = map[string]any{"id": id, "status": "ACTIVE", "metadata": map[string]string{"vdi_service": "test-vdi", "vdi_desktop_id": fmt.Sprint(n)}}
			}
		})
		f.tick(1)
		clone := *f.worker
		f.worker = &clone
		f.tick(4)
		creates, deletes := f.cloud.Counts()
		if creates != 0 || deletes != 2 {
			t.Fatal(creates, deletes)
		}
		f.create(f.admin)
		f.create(f.admin)
		if len(f.list("/api/admin/events?action=DESKTOP_CREATE_FAILED", f.admin)) != 1 {
			t.Fatal("missing failure event")
		}
	})
}
func TestReservationReleaseUnderConcurrentCompletion(t *testing.T) {
	f := setup(t)
	n1 := f.create(f.admin)
	n2 := f.create(f.admin)
	f.tick(4)
	f.request("DELETE", path(n1), f.admin, nil, "", 202)
	f.request("DELETE", path(n2), f.admin, nil, "", 202)
	// Concurrent finalizers write the same user's reservation and event counter,
	// exercising real transaction conflict/retry behavior.
	for range 4 {
		f.now = f.now.Add(time.Second)
		var wg sync.WaitGroup
		for range 4 {
			wg.Add(1)
			go func() {
				defer wg.Done()
				if e := f.worker.Tick(context.Background()); e != nil {
					t.Error(e)
				}
			}()
		}
		wg.Wait()
	}
	f.request("GET", path(n1), f.admin, nil, "", 404)
	f.request("GET", path(n2), f.admin, nil, "", 404)
	f.create(f.admin)
	f.create(f.admin)
}
func TestReadinessTimeoutCauseAndPendingDeleteRecovery(t *testing.T) {
	f := setup(t)
	n := f.create(f.admin)
	f.tick(1)
	f.now = f.now.Add(2 * time.Minute)
	f.tick(1)
	v := f.request("GET", path(n), f.admin, nil, "", 200)
	if v["failure"].(map[string]any)["code"] != "RDP_READY_TIMEOUT" {
		t.Fatal(v)
	}
	f.tick(5)
	// Queued requests recover from a recreated worker, and can be deleted without any Nova POST.
	queued := f.create(f.admin)
	f.request("DELETE", path(queued), f.admin, nil, "", 202)
	f.tick(5)
	f.request("GET", path(queued), f.admin, nil, "", 404)
	creates, _ := f.cloud.Counts()
	if creates != 1 {
		t.Fatal("deleted queue created VM", creates)
	}
}
func TestMeErrorsAndNoBodyContracts(t *testing.T) {
	f := setup(t)
	v := f.request("GET", "/api/me", "", nil, "", 401)
	if v["code"] != "UNAUTHENTICATED" {
		t.Fatal(v)
	}
	f.request("GET", "/api/desktops?userId=1", f.admin, nil, "", 400)
	f.request("DELETE", fmt.Sprintf("/api/admin/users/%d", f.adminID), f.admin, map[string]any{}, "", 400)
	f.request("POST", "/api/auth/logout", f.admin, map[string]any{}, "", 400)
	f.request("GET", "/api/me", f.admin, nil, "", 200)
	f.now = f.now.Add(8 * time.Hour)
	v = f.request("GET", "/api/me", f.admin, nil, "", 401)
	if v["code"] != "SESSION_EXPIRED" {
		t.Fatal(v)
	}
	v = f.request("POST", "/api/auth/logout", f.admin, nil, "", 401)
	if v["code"] != "UNAUTHORIZED" {
		t.Fatal(v)
	}
}
func TestCreateTransactionRollbackAndUserDeleteSession(t *testing.T) {
	f := setup(t)
	uid, user := f.user("user@example.com", "USER")
	_, e := f.store.DB.RunCommand(context.Background(), bson.D{{Key: "collMod", Value: "events"}, {Key: "validator", Value: bson.M{"action": bson.M{"$ne": "DESKTOP_CREATE"}}}}).Raw()
	if e != nil {
		t.Fatal(e)
	}
	f.request("POST", "/api/desktops", user, f.body(), uuid.NewString(), 500)
	if len(f.list("/api/desktops", user)) != 0 {
		t.Fatal("partial desktop")
	}
	_, e = f.store.DB.RunCommand(context.Background(), bson.D{{Key: "collMod", Value: "events"}, {Key: "validator", Value: bson.M{}}}).Raw()
	if e != nil {
		t.Fatal(e)
	}
	n := f.create(user)
	f.create(user)
	deleted := f.request("DELETE", fmt.Sprintf("/api/admin/users/%d", uid), f.admin, nil, "", 200)
	if len(deleted["reclaimedDesktopIds"].([]any)) != 2 {
		t.Fatal(deleted)
	}
	f.request("GET", path(n), user, nil, "", 401)
	f.request("GET", "/api/me", user, nil, "", 401)
}
func TestCreateResultSurvivesConcurrentDeletion(t *testing.T) {
	for _, rejected := range []bool{false, true} {
		t.Run(fmt.Sprintf("rejected=%v", rejected), func(t *testing.T) {
			f := setup(t)
			n := f.create(f.admin)
			started := make(chan struct{})
			continueCreate := make(chan struct{})
			f.cloud.Set(func(s *testinfra.OpenStack) {
				s.CreateStarted = started
				s.ContinueCreate = continueCreate
				s.RejectCreate = rejected
			})
			f.now = f.now.Add(time.Millisecond)
			done := make(chan error, 1)
			go func() { done <- f.worker.Tick(context.Background()) }()
			select {
			case <-started:
			case <-time.After(5 * time.Second):
				t.Fatal("create never started")
			}
			f.request("DELETE", path(n), f.admin, nil, "", 202)
			close(continueCreate)
			if e := <-done; e != nil {
				t.Fatal(e)
			}
			f.cloud.Set(func(s *testinfra.OpenStack) { s.CreateStarted = nil; s.ContinueCreate = nil; s.RejectCreate = false })
			f.tick(5)
			f.request("GET", path(n), f.admin, nil, "", 404)
			f.create(f.admin)
			f.create(f.admin)
		})
	}
}
func TestNovaGetOutageDoesNotExtendReadinessDeadline(t *testing.T) {
	f := setup(t)
	n := f.create(f.admin)
	f.tick(1)
	f.cloud.Set(func(s *testinfra.OpenStack) { s.FailGet = true })
	f.now = f.now.Add(2 * time.Minute)
	f.tick(1)
	v := f.request("GET", path(n), f.admin, nil, "", 200)
	if v["status"] != "ERROR" {
		t.Fatal(v)
	}
	f.request("POST", "/api/desktops", f.admin, f.body(), uuid.NewString(), 409)
	f.cloud.Set(func(s *testinfra.OpenStack) { s.FailGet = false })
	f.tick(5)
	f.create(f.admin)
}

func TestIdempotencyPreservesExactTimestamp(t *testing.T) {
	f := setup(t)
	f.now = f.now.Add(123456 * time.Nanosecond)
	key := uuid.NewString()
	first := f.request("POST", "/api/desktops", f.admin, f.body(), key, 202)
	second := f.request("POST", "/api/desktops", f.admin, f.body(), key, 202)
	if !reflect.DeepEqual(first, second) {
		t.Fatal("timestamp changed on replay", first, second)
	}
}
