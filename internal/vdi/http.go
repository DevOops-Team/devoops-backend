package vdi

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"devoops/vdi/internal/guacamole"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"github.com/google/uuid"
	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"
	"golang.org/x/crypto/bcrypt"
	"io"
	"mime"
	"net/http"
	"net/mail"
	"strconv"
	"strings"
	"time"
)

type App struct {
	Store     *Store
	Cloud     Cloud
	Guacamole *guacamole.Issuer
	Now       func() time.Time
	dummyHash []byte
}

func NewApp(s *Store, c Cloud, g *guacamole.Issuer) *App {
	h, _ := bcrypt.GenerateFromPassword([]byte("not-a-real-password"), bcrypt.DefaultCost)
	return &App{s, c, g, time.Now, h}
}
func tokenHash(t string) string { v := sha256.Sum256([]byte(t)); return hex.EncodeToString(v[:]) }
func bearer(r *http.Request) string {
	p := strings.Fields(r.Header.Get("Authorization"))
	if len(p) != 2 || !strings.EqualFold(p[0], "Bearer") {
		return ""
	}
	return tokenHash(p[1])
}
func (a *App) authorize(ctx context.Context, hash string, admin, touch bool) (User, error) {
	var sess Session
	if hash == "" {
		return User{}, Err(401, "UNAUTHORIZED", "로그인이 필요합니다.")
	}
	e := a.Store.C("sessions").FindOne(ctx, bson.M{"hash": hash, "expiresAt": bson.M{"$gt": a.Now().UTC().Truncate(time.Millisecond)}}).Decode(&sess)
	if errors.Is(e, mongo.ErrNoDocuments) {
		return User{}, Err(401, "UNAUTHORIZED", "로그인이 필요합니다.")
	}
	if e != nil {
		return User{}, e
	}
	u, e := a.Store.User(ctx, sess.UserID)
	if e != nil {
		var api *APIError
		if errors.As(e, &api) && api.Status == 404 {
			return User{}, Err(401, "UNAUTHORIZED", "로그인이 필요합니다.")
		}
		return User{}, e
	}
	if admin && u.Role != "ADMIN" {
		return User{}, Err(403, "ADMIN_ONLY", "관리자 권한이 필요합니다.")
	}
	if touch {
		if _, e = a.Store.C("sessions").UpdateOne(ctx, bson.M{"hash": hash}, bson.M{"$inc": bson.M{"version": 1}}); e != nil {
			return User{}, e
		}
		if _, e = a.Store.C("users").UpdateOne(ctx, bson.M{"id": u.ID}, bson.M{"$inc": bson.M{"version": 1}}); e != nil {
			return User{}, e
		}
		if admin {
			if _, e = a.Store.C("counters").UpdateOne(ctx, bson.M{"_id": "adminGuard"}, bson.M{"$inc": bson.M{"value": 1}}); e != nil {
				return User{}, e
			}
		}
	}
	return u, nil
}

type endpoint func(http.ResponseWriter, *http.Request, User) error

func respond(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	if status != 204 {
		_ = json.NewEncoder(w).Encode(v)
	}
}
func failure(w http.ResponseWriter, e error) {
	var api *APIError
	if !errors.As(e, &api) {
		api = Err(500, "INTERNAL_ERROR", "내부 오류가 발생했습니다.")
	}
	respond(w, api.Status, api)
}
func (a *App) wrap(admin bool, f endpoint) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
		defer cancel()
		r = r.WithContext(ctx)
		u, e := a.authorize(ctx, bearer(r), admin, false)
		if e != nil && r.URL.Path == "/api/me" {
			var api *APIError
			if errors.As(e, &api) && api.Status == 401 {
				code := "UNAUTHENTICATED"
				var sess Session
				lookup := a.Store.C("sessions").FindOne(ctx, bson.M{"hash": bearer(r)}).Decode(&sess)
				if lookup == nil && !a.Now().UTC().Truncate(time.Millisecond).Before(sess.ExpiresAt) {
					code = "SESSION_EXPIRED"
				}
				if lookup != nil && !errors.Is(lookup, mongo.ErrNoDocuments) {
					e = lookup
				} else {
					e = Err(401, code, "로그인이 필요합니다.")
				}
			}
		}
		if e == nil {
			e = validateQuery(r)
		}
		if e == nil {
			e = f(w, r, u)
		}
		if e != nil {
			failure(w, e)
		}
	}
}
func (a *App) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/auth/login", func(w http.ResponseWriter, r *http.Request) {
		if e := a.login(w, r); e != nil {
			failure(w, e)
		}
	})
	mux.HandleFunc("POST /api/auth/logout", a.wrap(false, a.logout))
	mux.HandleFunc("GET /api/me", a.wrap(false, func(w http.ResponseWriter, r *http.Request, u User) error { respond(w, 200, u); return nil }))
	mux.HandleFunc("GET /api/images", a.wrap(false, a.images))
	mux.HandleFunc("GET /api/images/{osId}/spec", a.wrap(false, a.imageSpec))
	mux.HandleFunc("GET /api/desktops", a.wrap(false, a.desktops))
	mux.HandleFunc("POST /api/desktops", a.wrap(false, a.create))
	mux.HandleFunc("GET /api/desktops/{desktopId}", a.wrap(false, a.desktop))
	mux.HandleFunc("DELETE /api/desktops/{desktopId}", a.wrap(false, a.deleteDesktop))
	mux.HandleFunc("POST /api/desktops/{desktopId}/connect", a.wrap(false, a.connect))
	mux.HandleFunc("GET /api/admin/summary", a.wrap(true, a.summary))
	mux.HandleFunc("GET /api/admin/users", a.wrap(true, a.users))
	mux.HandleFunc("POST /api/admin/users", a.wrap(true, a.createUser))
	mux.HandleFunc("PATCH /api/admin/users/{userId}", a.wrap(true, a.updateUser))
	mux.HandleFunc("DELETE /api/admin/users/{userId}", a.wrap(true, a.deleteUser))
	mux.HandleFunc("GET /api/admin/desktops", a.wrap(true, a.desktops))
	mux.HandleFunc("POST /api/admin/desktops", a.wrap(true, a.create))
	mux.HandleFunc("DELETE /api/admin/desktops/{desktopId}", a.wrap(true, a.deleteDesktop))
	mux.HandleFunc("GET /api/admin/events", a.wrap(true, a.events))
	// ServeMux defaults produce text/html errors; retain the common JSON error contract.
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if recover() != nil {
				failure(w, errors.New("panic"))
			}
		}()
		h, p := mux.Handler(r)
		if p == "" {
			status := 404
			probe := *r
			for _, method := range []string{"GET", "POST", "PATCH", "DELETE"} {
				probe.Method = method
				if _, match := mux.Handler(&probe); match != "" {
					status = 405
					break
				}
			}
			failure(w, Err(status, "NOT_FOUND", "요청 경로 또는 메서드를 확인해 주세요."))
			return
		}
		_ = h
		mux.ServeHTTP(w, r)
	})
}
func decode(w http.ResponseWriter, r *http.Request, v any) error {
	if len(r.URL.Query()) > 0 {
		return Invalid("query", "쿼리를 허용하지 않습니다.")
	}
	typ, _, e := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if e != nil || typ != "application/json" {
		return Invalid("body", "application/json이 필요합니다.")
	}
	d := json.NewDecoder(http.MaxBytesReader(w, r.Body, 65536))
	d.DisallowUnknownFields()
	if e = d.Decode(v); e != nil {
		return Invalid("body", "JSON 필드와 타입을 확인해 주세요.")
	}
	if e = d.Decode(new(any)); e != io.EOF {
		return Invalid("body", "JSON 객체 하나가 필요합니다.")
	}
	return nil
}
func noBody(r *http.Request) error {
	if len(r.URL.Query()) > 0 {
		return Invalid("query", "쿼리를 허용하지 않습니다.")
	}
	b, e := io.ReadAll(io.LimitReader(r.Body, 2))
	if e != nil || len(b) > 0 {
		return Invalid("body", "본문을 허용하지 않습니다.")
	}
	return nil
}
func validEmail(v string) bool {
	m, e := mail.ParseAddress(v)
	return e == nil && m.Address == v && strings.Contains(v, "@") && len(v) <= 254
}
func id(v, field string) (int64, error) {
	n, e := strconv.ParseInt(v, 10, 64)
	if e != nil || n <= 0 || n > MaxID {
		return 0, Invalid(field, "양의 안전 정수가 필요합니다.")
	}
	return n, nil
}
func (a *App) login(w http.ResponseWriter, r *http.Request) error {
	var req struct {
		Email    string `json:"email"`
		Password string `json:"password"`
	}
	if e := decode(w, r, &req); e != nil {
		return e
	}
	if !validEmail(req.Email) {
		return Invalid("email", "유효한 이메일이 필요합니다.")
	}
	if req.Password == "" || len(req.Password) > 72 {
		return Invalid("password", "1~72 byte 비밀번호가 필요합니다.")
	}
	var u User
	e := a.Store.C("users").FindOne(r.Context(), bson.M{"email": strings.ToLower(req.Email), "deleted": false, "internal": false}).Decode(&u)
	if e != nil && !errors.Is(e, mongo.ErrNoDocuments) {
		return e
	}
	hash := u.PasswordHash
	if len(hash) == 0 {
		hash = a.dummyHash
	}
	passwordErr := bcrypt.CompareHashAndPassword(hash, []byte(req.Password))
	if e != nil || passwordErr != nil {
		return Err(401, "INVALID_CREDENTIALS", "이메일 또는 비밀번호가 올바르지 않습니다.")
	}
	raw := make([]byte, 32)
	if _, e = rand.Read(raw); e != nil {
		return e
	}
	token := base64.RawURLEncoding.EncodeToString(raw)
	now := a.Now().UTC().Truncate(time.Millisecond)
	expires := now.Add(8 * time.Hour)
	e = a.Store.Tx(r.Context(), func(c context.Context) error {
		current, e := a.Store.User(c, u.ID)
		if e != nil {
			return Err(401, "INVALID_CREDENTIALS", "이메일 또는 비밀번호가 올바르지 않습니다.")
		}
		if string(current.PasswordHash) != string(u.PasswordHash) || current.Role != u.Role {
			return Err(401, "INVALID_CREDENTIALS", "이메일 또는 비밀번호가 올바르지 않습니다.")
		}
		u = current
		if _, e = a.Store.C("users").UpdateOne(c, bson.M{"id": u.ID}, bson.M{"$inc": bson.M{"version": 1}}); e != nil {
			return e
		}
		if _, e = a.Store.C("sessions").InsertOne(c, Session{tokenHash(token), u.ID, expires}); e != nil {
			return e
		}
		return a.Store.Event(c, u.ID, nil, "LOGIN", "USER", "로그인", now)
	})
	if e != nil {
		return e
	}
	respond(w, 200, map[string]any{"token": token, "expiresAt": expires, "user": u})
	return nil
}
func (a *App) logout(w http.ResponseWriter, r *http.Request, _ User) error {
	if e := noBody(r); e != nil {
		return e
	}
	e := a.Store.Tx(r.Context(), func(c context.Context) error {
		u, e := a.authorize(c, bearer(r), false, true)
		if e != nil {
			return e
		}
		if _, e = a.Store.C("sessions").DeleteOne(c, bson.M{"hash": bearer(r)}); e != nil {
			return e
		}
		return a.Store.Event(c, u.ID, nil, "LOGOUT", "USER", "로그아웃", a.Now())
	})
	if e != nil {
		return e
	}
	respond(w, 204, nil)
	return nil
}
func (a *App) images(w http.ResponseWriter, r *http.Request, _ User) error {
	if e := noBody(r); e != nil {
		return e
	}
	list, e := all[OS](r.Context(), a.Store.C("os"), bson.M{}, options.Find().SetSort(bson.D{{Key: "id", Value: 1}}))
	if e != nil {
		return e
	}
	out := []OS{}
	for _, im := range list {
		_, e := a.Cloud.Spec(r.Context(), im.ImageID)
		if e == nil {
			out = append(out, im)
		} else if !errors.Is(e, ErrOSUnavailable) && !errors.Is(e, ErrSpecUnavailable) {
			return e
		}
	}
	respond(w, 200, out)
	return nil
}

// The create form must use the actual flavor/image combination, rather than
// guessing a CPU, RAM or disk size. Keep infrastructure IDs private.
func (a *App) imageSpec(w http.ResponseWriter, r *http.Request, _ User) error {
	if e := noBody(r); e != nil {
		return e
	}
	n, e := id(r.PathValue("osId"), "osId")
	if e != nil {
		return e
	}
	var im OS
	e = a.Store.C("os").FindOne(r.Context(), bson.M{"id": n}).Decode(&im)
	if errors.Is(e, mongo.ErrNoDocuments) {
		return Err(404, "OS_NOT_FOUND", "OS를 찾을 수 없습니다.")
	}
	if e != nil {
		return e
	}
	spec, e := a.Cloud.Spec(r.Context(), im.ImageID)
	if e != nil {
		return e
	}
	respond(w, 200, map[string]any{"osId": n, "cpuCores": spec.CPUCores, "memoryGb": spec.MemoryGB, "storageGb": spec.StorageGB})
	return nil
}

func (a *App) desktops(w http.ResponseWriter, r *http.Request, u User) error {
	f := bson.M{"deleted": false}
	if strings.HasPrefix(r.URL.Path, "/api/admin/") {
		if v, ok := r.URL.Query()["userId"]; ok {
			if len(v) != 1 {
				return Invalid("userId", "하나의 값만 허용합니다.")
			}
			n, e := id(v[0], "userId")
			if e != nil {
				return e
			}
			f["userId"] = n
		}
	} else {
		f["userId"] = u.ID
	}
	list, e := all[Desktop](r.Context(), a.Store.C("desktops"), f, options.Find().SetSort(bson.D{{Key: "createdAt", Value: -1}, {Key: "id", Value: -1}}))
	if e != nil {
		return e
	}
	out := []DesktopResponse{}
	for _, d := range list {
		out = append(out, d.Response(u.ID))
	}
	respond(w, 200, out)
	return nil
}
func (a *App) own(ctx context.Context, v string, u User) (Desktop, error) {
	n, e := id(v, "desktopId")
	if e != nil {
		return Desktop{}, e
	}
	d, e := a.Store.Desktop(ctx, n)
	if e == nil && d.UserID != u.ID {
		return Desktop{}, Err(404, "DESKTOP_NOT_FOUND", "데스크톱을 찾을 수 없습니다.")
	}
	return d, e
}
func (a *App) desktop(w http.ResponseWriter, r *http.Request, u User) error {
	if e := noBody(r); e != nil {
		return e
	}
	d, e := a.own(r.Context(), r.PathValue("desktopId"), u)
	if e != nil {
		return e
	}
	respond(w, 200, d.Response(u.ID))
	return nil
}
func (a *App) connect(w http.ResponseWriter, r *http.Request, u User) error {
	if e := noBody(r); e != nil {
		return e
	}
	d, e := a.own(r.Context(), r.PathValue("desktopId"), u)
	if e != nil {
		return e
	}
	if d.Status != "RUNNING" || d.DeleteRequested {
		return Err(409, "DESKTOP_NOT_RUNNING", "데스크톱이 실행 중이 아닙니다.")
	}
	vm, e := a.Cloud.Get(r.Context(), d.VMID)
	if e != nil {
		if errors.Is(e, ErrVMNotFound) {
			return Err(409, "DESKTOP_NOT_RUNNING", "데스크톱이 실행 중이 아닙니다.")
		}
		return e
	}
	if vm.Status != "ACTIVE" {
		return Err(409, "DESKTOP_NOT_RUNNING", "데스크톱이 실행 중이 아닙니다.")
	}
	if !a.Cloud.Ready(r.Context(), vm.IP) {
		return Err(409, "DESKTOP_NOT_READY", "원격 데스크톱이 아직 준비되지 않았습니다.")
	}
	if a.Guacamole == nil {
		return Err(503, "GUACAMOLE_UNAVAILABLE", "접속 서비스를 사용할 수 없습니다.")
	}
	address, expires, e := a.Guacamole.Issue(u.ID, d.ID, d.Name, d.OS.Type, vm.IP, a.Now())
	if e != nil {
		return Err(503, "GUACAMOLE_UNAVAILABLE", "접속 서비스를 사용할 수 없습니다.")
	}
	// Recheck authorization and deletion after network calls, before issuing the capability.
	if _, e = a.authorize(r.Context(), bearer(r), false, false); e != nil {
		return e
	}
	fresh, e := a.Store.Desktop(r.Context(), d.ID)
	if e != nil {
		return e
	}
	if fresh.DeleteRequested || fresh.Status != "RUNNING" {
		return Err(409, "DESKTOP_NOT_RUNNING", "데스크톱이 실행 중이 아닙니다.")
	}
	respond(w, 200, map[string]any{"url": address, "expiresAt": expires})
	return nil
}
func validateCreate(q CreateRequest) error {
	if strings.TrimSpace(q.Name) == "" {
		return Invalid("name", "이름이 필요합니다.")
	}
	if q.OSID <= 0 || q.OSID > MaxID {
		return Invalid("osId", "양의 안전 정수가 필요합니다.")
	}
	if q.CPUCores <= 0 {
		return Invalid("cpuCores", "양의 정수가 필요합니다.")
	}
	if q.MemoryGB <= 0 {
		return Invalid("memoryGb", "양의 정수가 필요합니다.")
	}
	if q.StorageGB <= 0 {
		return Invalid("storageGb", "양의 정수가 필요합니다.")
	}
	return nil
}
func (a *App) create(w http.ResponseWriter, r *http.Request, u User) error {
	admin := strings.HasPrefix(r.URL.Path, "/api/admin/")
	q := CreateRequest{}
	target := u.ID
	if admin {
		var req struct {
			CreateRequest
			UserID int64 `json:"userId"`
		}
		if e := decode(w, r, &req); e != nil {
			return e
		}
		q = req.CreateRequest
		target = req.UserID
		if target <= 0 || target > MaxID {
			return Invalid("userId", "양의 안전 정수가 필요합니다.")
		}
	} else {
		if e := decode(w, r, &q); e != nil {
			return e
		}
	}
	if e := validateCreate(q); e != nil {
		return e
	}
	key := r.Header.Get("Idempotency-Key")
	if _, e := uuid.Parse(key); e != nil || len(key) != 36 {
		return Invalid("Idempotency-Key", "UUID가 필요합니다.")
	}
	key = strings.ToLower(key)
	body, _ := json.Marshal(struct {
		Path    string
		Target  int64
		Request CreateRequest
	}{r.URL.Path, target, q})
	fingerprint := tokenHash(string(body))
	now := a.Now().UTC().Truncate(time.Millisecond)
	var response DesktopResponse
	// Replays must work even when current quota or infrastructure availability has changed.
	replay := func(c context.Context) (bool, error) {
		var old Idempotency
		e := a.Store.C("idempotency").FindOne(c, bson.M{"actorId": u.ID, "key": key, "expiresAt": bson.M{"$gt": now}}).Decode(&old)
		if errors.Is(e, mongo.ErrNoDocuments) {
			return false, nil
		}
		if e != nil {
			return false, e
		}
		if old.Fingerprint != fingerprint {
			return false, Err(409, "IDEMPOTENCY_CONFLICT", "동일 키에 다른 요청을 사용할 수 없습니다.")
		}
		response = old.Response
		return true, nil
	}
	if ok, e := replay(r.Context()); e != nil {
		return e
	} else if ok {
		respond(w, 202, response)
		return nil
	}
	var im OS
	e := a.Store.C("os").FindOne(r.Context(), bson.M{"id": q.OSID}).Decode(&im)
	if errors.Is(e, mongo.ErrNoDocuments) {
		return Err(404, "OS_NOT_FOUND", "OS를 찾을 수 없습니다.")
	}
	if e != nil {
		return e
	}
	spec, e := a.Cloud.Spec(r.Context(), im.ImageID)
	if e != nil {
		return e
	}
	if q.CPUCores != spec.CPUCores || q.MemoryGB != spec.MemoryGB || q.StorageGB != spec.StorageGB {
		return Invalid("spec", "허용되지 않는 사양입니다.")
	}
	e = a.Store.Tx(r.Context(), func(c context.Context) error {
		actor, e := a.authorize(c, bearer(r), admin, true)
		if e != nil {
			return e
		}
		u = actor
		if ok, e := replay(c); e != nil || ok {
			return e
		}
		if _, e = a.Store.User(c, target); e != nil {
			return e
		}
		// Any cleanup-pending failed VM must be resolved before accepting a fresh retry.
		pending, e := a.Store.C("desktops").CountDocuments(c, bson.M{"userId": target, "reserved": true, "status": "ERROR"})
		if e != nil {
			return e
		}
		if pending > 0 {
			return Err(409, "OPERATION_IN_PROGRESS", "실패 자원 정리가 진행 중입니다.")
		}
		res, e := a.Store.C("users").UpdateOne(c, bson.M{"id": target, "deleted": false, "reserved": bson.M{"$lt": 2}}, bson.M{"$inc": bson.M{"reserved": 1, "version": 1}})
		if e != nil {
			return e
		}
		if res.ModifiedCount == 0 {
			return Err(409, "DESKTOP_QUOTA_EXCEEDED", "최대 2대까지 생성할 수 있습니다.")
		}
		n, e := a.Store.NextID(c)
		if e != nil {
			return e
		}
		response = DesktopResponse{ID: n, UserID: target, Name: q.Name, OSID: q.OSID, CPUCores: q.CPUCores, MemoryGB: q.MemoryGB, StorageGB: q.StorageGB, OS: im, Status: "CREATING", ConnectionState: "PENDING", CreatedAt: now, UpdatedAt: now}
		d := Desktop{DesktopResponse: response, ImageID: im.ImageID, FlavorID: spec.FlavorID, Phase: "queued", Reserved: true, NextRun: now}
		if _, e = a.Store.C("desktops").InsertOne(c, d); e != nil {
			return e
		}
		// Expired entries may remain until Mongo TTL sweeps; replace them explicitly.
		if _, e = a.Store.C("idempotency").ReplaceOne(c, bson.M{"actorId": u.ID, "key": key}, Idempotency{u.ID, key, fingerprint, response, now.Add(24 * time.Hour)}, options.Replace().SetUpsert(true)); e != nil {
			return e
		}
		action := "DESKTOP_CREATE"
		if admin {
			action = "DESKTOP_ASSIGN"
		}
		return a.Store.Event(c, u.ID, &n, action, "DESKTOP", "데스크톱 생성 요청 접수", now)
	})
	if e != nil {
		return e
	}
	respond(w, 202, response)
	return nil
}
func (a *App) requestDelete(c context.Context, d Desktop, actor int64) error {
	d.Status = "DELETING"
	d.ConnectionState = "UNAVAILABLE"
	d.CanConnect = false
	d.DeleteRequested = true
	d.DeleteActor = actor
	d.Phase = "delete"
	d.NextRun = a.Now().UTC().Truncate(time.Millisecond)
	d.UpdatedAt = d.NextRun
	d.Revision++
	_, e := a.Store.C("desktops").ReplaceOne(c, bson.M{"id": d.ID}, d)
	return e
}
func (a *App) deleteDesktop(w http.ResponseWriter, r *http.Request, _ User) error {
	if e := noBody(r); e != nil {
		return e
	}
	n, e := id(r.PathValue("desktopId"), "desktopId")
	if e != nil {
		return e
	}
	admin := strings.HasPrefix(r.URL.Path, "/api/admin/")
	e = a.Store.Tx(r.Context(), func(c context.Context) error {
		u, e := a.authorize(c, bearer(r), admin, true)
		if e != nil {
			return e
		}
		d, e := a.Store.Desktop(c, n)
		if e != nil {
			return e
		}
		if !admin && d.UserID != u.ID {
			return Err(404, "DESKTOP_NOT_FOUND", "데스크톱을 찾을 수 없습니다.")
		}
		if d.DeleteRequested {
			return Err(409, "OPERATION_IN_PROGRESS", "삭제가 진행 중입니다.")
		}
		if e = a.requestDelete(c, d, u.ID); e != nil {
			return e
		}
		if admin {
			return a.Store.Event(c, u.ID, &n, "DESKTOP_RELEASE", "DESKTOP", "관리자 회수 요청 접수", a.Now())
		}
		return nil
	})
	if e != nil {
		return e
	}
	respond(w, 202, map[string]any{"id": n, "status": "DELETING"})
	return nil
}

func validateQuery(r *http.Request) error {
	allowed := map[string]bool{}
	if r.Method == "GET" && r.URL.Path == "/api/admin/desktops" {
		allowed["userId"] = true
	}
	if r.Method == "GET" && r.URL.Path == "/api/admin/events" {
		for _, k := range []string{"limit", "actorId", "desktopId", "action", "targetType"} {
			allowed[k] = true
		}
	}
	for k := range r.URL.Query() {
		if !allowed[k] {
			return Invalid(k, "허용되지 않는 쿼리입니다.")
		}
	}
	return nil
}
