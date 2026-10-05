package vdi

import (
	"context"
	"encoding/json"
	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"
	"golang.org/x/crypto/bcrypt"
	"net/http"
	"strconv"
	"strings"
	"time"
)

func (a *App) users(w http.ResponseWriter, r *http.Request, _ User) error {
	list, e := all[User](r.Context(), a.Store.C("users"), bson.M{"deleted": false, "internal": false}, options.Find().SetSort(bson.D{{Key: "id", Value: 1}}))
	if e != nil {
		return e
	}
	type entry struct {
		User
		DesktopCount int64 `json:"desktopCount"`
	}
	out := []entry{}
	for _, u := range list {
		n, e := a.Store.C("desktops").CountDocuments(r.Context(), bson.M{"userId": u.ID, "deleted": false})
		if e != nil {
			return e
		}
		out = append(out, entry{u, n})
	}
	respond(w, 200, out)
	return nil
}
func (a *App) createUser(w http.ResponseWriter, r *http.Request, _ User) error {
	var req struct {
		Name     string `json:"name"`
		Email    string `json:"email"`
		Password string `json:"password"`
		Role     string `json:"role"`
	}
	if e := decode(w, r, &req); e != nil {
		return e
	}
	if strings.TrimSpace(req.Name) == "" {
		return Invalid("name", "이름이 필요합니다.")
	}
	if !validEmail(req.Email) {
		return Invalid("email", "유효한 이메일이 필요합니다.")
	}
	if req.Role != "USER" && req.Role != "ADMIN" {
		return Invalid("role", "USER 또는 ADMIN이 필요합니다.")
	}
	if req.Password == "" || len(req.Password) > 72 {
		return Invalid("password", "1~72 byte 비밀번호가 필요합니다.")
	}
	hash, e := bcrypt.GenerateFromPassword([]byte(req.Password), bcrypt.DefaultCost)
	if e != nil {
		return e
	}
	var u User
	e = a.Store.Tx(r.Context(), func(c context.Context) error {
		if _, e := a.authorize(c, bearer(r), true, true); e != nil {
			return e
		}
		n, e := a.Store.NextID(c)
		if e != nil {
			return e
		}
		now := a.Now().UTC().Truncate(time.Millisecond)
		u = User{ID: n, Name: req.Name, Email: strings.ToLower(req.Email), Role: req.Role, PasswordHash: hash, CreatedAt: now, UpdatedAt: now}
		_, e = a.Store.C("users").InsertOne(c, u)
		return e
	})
	if mongo.IsDuplicateKeyError(e) {
		return Err(409, "EMAIL_ALREADY_EXISTS", "이미 사용 중인 이메일입니다.")
	}
	if e != nil {
		return e
	}
	respond(w, 201, u)
	return nil
}
func (a *App) updateUser(w http.ResponseWriter, r *http.Request, _ User) error {
	n, e := id(r.PathValue("userId"), "userId")
	if e != nil {
		return e
	}
	var req map[string]json.RawMessage
	if e = decode(w, r, &req); e != nil {
		return e
	}
	if len(req) == 0 {
		return Invalid("body", "최소 한 필드가 필요합니다.")
	}
	fields := map[string]string{}
	for k, v := range req {
		if k != "name" && k != "email" && k != "role" {
			return Invalid(k, "변경할 수 없는 필드입니다.")
		}
		var value string
		if string(v) == "null" || json.Unmarshal(v, &value) != nil {
			return Invalid(k, "문자열이 필요합니다.")
		}
		switch k {
		case "name":
			if strings.TrimSpace(value) == "" {
				return Invalid(k, "이름이 필요합니다.")
			}
		case "email":
			if !validEmail(value) {
				return Invalid(k, "유효한 이메일이 필요합니다.")
			}
			value = strings.ToLower(value)
		case "role":
			if value != "USER" && value != "ADMIN" {
				return Invalid(k, "USER 또는 ADMIN이 필요합니다.")
			}
		}
		fields[k] = value
	}
	var u User
	e = a.Store.Tx(r.Context(), func(c context.Context) error {
		if _, e := a.authorize(c, bearer(r), true, true); e != nil {
			return e
		}
		var e error
		u, e = a.Store.User(c, n)
		if e != nil {
			return e
		}
		roleChanged := false
		if role, ok := fields["role"]; ok && role != u.Role {
			if u.Role == "ADMIN" {
				count, e := a.Store.C("users").CountDocuments(c, bson.M{"role": "ADMIN", "deleted": false, "internal": false})
				if e != nil {
					return e
				}
				if count <= 1 {
					return Err(403, "LAST_ADMIN_PROTECTED", "마지막 관리자 권한은 제거할 수 없습니다.")
				}
			}
			u.Role = role
			roleChanged = true
		}
		if v, ok := fields["name"]; ok {
			u.Name = v
		}
		if v, ok := fields["email"]; ok {
			u.Email = v
		}
		u.UpdatedAt = a.Now().UTC().Truncate(time.Millisecond)
		u.Version++
		if _, e = a.Store.C("users").ReplaceOne(c, bson.M{"id": n}, u); e != nil {
			return e
		}
		if roleChanged {
			_, e = a.Store.C("sessions").DeleteMany(c, bson.M{"userId": n})
			return e
		}
		return nil
	})
	if mongo.IsDuplicateKeyError(e) {
		return Err(409, "EMAIL_ALREADY_EXISTS", "이미 사용 중인 이메일입니다.")
	}
	if e != nil {
		return e
	}
	respond(w, 200, u)
	return nil
}
func (a *App) deleteUser(w http.ResponseWriter, r *http.Request, _ User) error {
	if e := noBody(r); e != nil {
		return e
	}
	n, e := id(r.PathValue("userId"), "userId")
	if e != nil {
		return e
	}
	reclaimed := []int64{}
	e = a.Store.Tx(r.Context(), func(c context.Context) error {
		reclaimed = []int64{}
		actor, e := a.authorize(c, bearer(r), true, true)
		if e != nil {
			return e
		}
		if actor.ID == n {
			return Err(403, "SELF_DELETE_FORBIDDEN", "관리자는 자신을 삭제할 수 없습니다.")
		}
		u, e := a.Store.User(c, n)
		if e != nil {
			return e
		}
		if u.Role == "ADMIN" {
			count, e := a.Store.C("users").CountDocuments(c, bson.M{"role": "ADMIN", "deleted": false, "internal": false})
			if e != nil {
				return e
			}
			if count <= 1 {
				return Err(403, "LAST_ADMIN_PROTECTED", "마지막 관리자는 삭제할 수 없습니다.")
			}
		}
		if _, e = a.Store.C("users").UpdateOne(c, bson.M{"id": n}, bson.M{"$set": bson.M{"deleted": true, "updatedAt": a.Now().UTC().Truncate(time.Millisecond)}, "$inc": bson.M{"version": 1}}); e != nil {
			return e
		}
		if _, e = a.Store.C("sessions").DeleteMany(c, bson.M{"userId": n}); e != nil {
			return e
		}
		list, e := all[Desktop](c, a.Store.C("desktops"), bson.M{"userId": n, "deleted": false}, options.Find().SetSort(bson.D{{Key: "id", Value: 1}}))
		if e != nil {
			return e
		}
		for _, d := range list {
			reclaimed = append(reclaimed, d.ID)
			if !d.DeleteRequested {
				if e = a.requestDelete(c, d, actor.ID); e != nil {
					return e
				}
				if e = a.Store.Event(c, actor.ID, &d.ID, "DESKTOP_RELEASE", "DESKTOP", "사용자 삭제에 따른 회수 요청", a.Now()); e != nil {
					return e
				}
			}
		}
		return nil
	})
	if e != nil {
		return e
	}
	respond(w, 200, map[string]any{"userId": n, "reclaimedDesktopIds": reclaimed})
	return nil
}
func (a *App) summary(w http.ResponseWriter, r *http.Request, _ User) error {
	type summary struct {
		Users    int64          `json:"users"`
		Admins   int64          `json:"admins"`
		Desktops int            `json:"desktops"`
		ByStatus map[string]int `json:"byStatus"`
		ByOS     map[string]int `json:"byOs"`
		ByNode   map[string]int `json:"byNode"`
	}
	out := summary{ByStatus: map[string]int{}, ByOS: map[string]int{}, ByNode: map[string]int{}}
	// Snapshot ensures counts describe one consistent point in time.
	e := a.Store.Tx(r.Context(), func(c context.Context) error {
		var e error
		out.Users, e = a.Store.C("users").CountDocuments(c, bson.M{"deleted": false, "internal": false})
		if e != nil {
			return e
		}
		out.Admins, e = a.Store.C("users").CountDocuments(c, bson.M{"deleted": false, "internal": false, "role": "ADMIN"})
		if e != nil {
			return e
		}
		list, e := all[Desktop](c, a.Store.C("desktops"), bson.M{"deleted": false})
		if e != nil {
			return e
		}
		out.Desktops = len(list)
		out.ByStatus = map[string]int{}
		out.ByOS = map[string]int{}
		out.ByNode = map[string]int{}
		for _, d := range list {
			out.ByStatus[d.Status]++
			out.ByOS[d.OS.Type]++
			node := "-"
			if d.NodeName != nil {
				node = *d.NodeName
			}
			out.ByNode[node]++
		}
		return nil
	})
	if e != nil {
		return e
	}
	respond(w, 200, out)
	return nil
}

var actions = map[string]bool{"LOGIN": true, "LOGOUT": true, "DESKTOP_CREATE": true, "DESKTOP_DELETE": true, "DESKTOP_START": true, "DESKTOP_STOP": true, "DESKTOP_CREATE_FAILED": true, "DESKTOP_DELETE_FAILED": true, "DESKTOP_ASSIGN": true, "DESKTOP_RELEASE": true, "SYSTEM_ERROR": true}
var targets = map[string]bool{"USER": true, "DESKTOP": true, "SYSTEM": true, "OPENSTACK": true}

func (a *App) events(w http.ResponseWriter, r *http.Request, _ User) error {
	f := bson.M{}
	limit := int64(100)
	for k, values := range r.URL.Query() {
		if len(values) != 1 {
			return Invalid(k, "하나의 값만 허용합니다.")
		}
		v := values[0]
		switch k {
		case "actorId", "desktopId":
			n, e := id(v, k)
			if e != nil {
				return e
			}
			f[k] = n
		case "action":
			if !actions[v] {
				return Invalid(k, "허용되지 않는 이벤트입니다.")
			}
			f[k] = v
		case "targetType":
			if !targets[v] {
				return Invalid(k, "허용되지 않는 대상입니다.")
			}
			f[k] = v
		case "limit":
			n, e := strconv.ParseInt(v, 10, 64)
			if e != nil || n < 1 || n > 500 {
				return Invalid(k, "1~500 범위가 필요합니다.")
			}
			limit = n
		default:
			return Invalid(k, "허용되지 않는 필터입니다.")
		}
	}
	list, e := all[Event](r.Context(), a.Store.C("events"), f, options.Find().SetSort(bson.D{{Key: "createdAt", Value: -1}, {Key: "id", Value: -1}}).SetLimit(limit))
	if e != nil {
		return e
	}
	respond(w, 200, list)
	return nil
}
