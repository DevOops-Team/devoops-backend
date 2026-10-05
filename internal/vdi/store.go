package vdi

import (
	"context"
	"errors"
	"fmt"
	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"
	"go.mongodb.org/mongo-driver/v2/mongo/readconcern"
	"go.mongodb.org/mongo-driver/v2/mongo/writeconcern"
	"golang.org/x/crypto/bcrypt"
	"time"
)

type Store struct {
	DB       *mongo.Database
	SystemID int64
}

func OpenStore(ctx context.Context, uri, name string) (*Store, error) {
	c, e := mongo.Connect(options.Client().ApplyURI(uri).SetServerSelectionTimeout(10 * time.Second))
	if e != nil {
		return nil, e
	}
	if e = c.Ping(ctx, nil); e != nil {
		_ = c.Disconnect(ctx)
		return nil, e
	}
	return &Store{DB: c.Database(name)}, nil
}
func (s *Store) C(name string) *mongo.Collection { return s.DB.Collection(name) }
func (s *Store) Tx(ctx context.Context, fn func(context.Context) error) error {
	sess, e := s.DB.Client().StartSession()
	if e != nil {
		return e
	}
	defer sess.EndSession(ctx)
	_, e = sess.WithTransaction(ctx, func(c context.Context) (any, error) { return nil, fn(c) }, options.Transaction().SetReadConcern(readconcern.Snapshot()).SetWriteConcern(writeconcern.Majority()))
	return e
}
func (s *Store) NextID(ctx context.Context) (int64, error) {
	var v struct {
		Value int64 `bson:"value"`
	}
	e := s.C("counters").FindOneAndUpdate(ctx, bson.M{"_id": "public"}, bson.M{"$inc": bson.M{"value": 1}}, options.FindOneAndUpdate().SetUpsert(true).SetReturnDocument(options.After)).Decode(&v)
	if e == nil && (v.Value <= 0 || v.Value > MaxID) {
		return 0, fmt.Errorf("public ID range exhausted")
	}
	return v.Value, e
}
func (s *Store) Event(ctx context.Context, actor int64, desktop *int64, action, target, detail string, now time.Time) error {
	id, e := s.NextID(ctx)
	if e != nil {
		return e
	}
	_, e = s.C("events").InsertOne(ctx, Event{id, actor, desktop, action, &target, &detail, now.UTC().Truncate(time.Millisecond)})
	return e
}
func (s *Store) User(ctx context.Context, id int64) (User, error) {
	var u User
	e := s.C("users").FindOne(ctx, bson.M{"id": id, "deleted": false, "internal": false}).Decode(&u)
	if errors.Is(e, mongo.ErrNoDocuments) {
		return u, Err(404, "USER_NOT_FOUND", "사용자를 찾을 수 없습니다.")
	}
	return u, e
}
func (s *Store) Desktop(ctx context.Context, id int64) (Desktop, error) {
	var d Desktop
	e := s.C("desktops").FindOne(ctx, bson.M{"id": id, "deleted": false}).Decode(&d)
	if errors.Is(e, mongo.ErrNoDocuments) {
		return d, Err(404, "DESKTOP_NOT_FOUND", "데스크톱을 찾을 수 없습니다.")
	}
	return d, e
}
func all[T any](ctx context.Context, c *mongo.Collection, f any, opts ...options.Lister[options.FindOptions]) ([]T, error) {
	out := []T{}
	cur, e := c.Find(ctx, f, opts...)
	if e != nil {
		return nil, e
	}
	defer cur.Close(ctx)
	e = cur.All(ctx, &out)
	return out, e
}
func (s *Store) Bootstrap(ctx context.Context, name, email, password string, images []OS) error {
	indexes := map[string][]mongo.IndexModel{
		"users":       {{Keys: bson.D{{Key: "id", Value: 1}}, Options: options.Index().SetUnique(true)}, {Keys: bson.D{{Key: "email", Value: 1}}, Options: options.Index().SetUnique(true)}, {Keys: bson.D{{Key: "role", Value: 1}, {Key: "deleted", Value: 1}}}},
		"os":          {{Keys: bson.D{{Key: "id", Value: 1}}, Options: options.Index().SetUnique(true)}, {Keys: bson.D{{Key: "type", Value: 1}}, Options: options.Index().SetUnique(true)}, {Keys: bson.D{{Key: "imageId", Value: 1}}, Options: options.Index().SetUnique(true)}},
		"sessions":    {{Keys: bson.D{{Key: "hash", Value: 1}}, Options: options.Index().SetUnique(true)}, {Keys: bson.D{{Key: "userId", Value: 1}}}, {Keys: bson.D{{Key: "expiresAt", Value: 1}}, Options: options.Index().SetExpireAfterSeconds(0)}},
		"idempotency": {{Keys: bson.D{{Key: "actorId", Value: 1}, {Key: "key", Value: 1}}, Options: options.Index().SetUnique(true)}, {Keys: bson.D{{Key: "expiresAt", Value: 1}}, Options: options.Index().SetExpireAfterSeconds(0)}},
		"desktops":    {{Keys: bson.D{{Key: "id", Value: 1}}, Options: options.Index().SetUnique(true)}, {Keys: bson.D{{Key: "userId", Value: 1}, {Key: "deleted", Value: 1}}}, {Keys: bson.D{{Key: "status", Value: 1}}}, {Keys: bson.D{{Key: "phase", Value: 1}, {Key: "nextRun", Value: 1}, {Key: "leaseUntil", Value: 1}}}},
		"events":      {{Keys: bson.D{{Key: "id", Value: 1}}, Options: options.Index().SetUnique(true)}, {Keys: bson.D{{Key: "createdAt", Value: -1}}}, {Keys: bson.D{{Key: "actorId", Value: 1}, {Key: "createdAt", Value: -1}}}, {Keys: bson.D{{Key: "desktopId", Value: 1}, {Key: "createdAt", Value: -1}}}, {Keys: bson.D{{Key: "action", Value: 1}, {Key: "targetType", Value: 1}, {Key: "createdAt", Value: -1}}}},
	}
	for n, ix := range indexes {
		if _, e := s.C(n).Indexes().CreateMany(ctx, ix); e != nil {
			return e
		}
	}
	hash, e := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	if e != nil {
		return e
	}
	e = s.Tx(ctx, func(c context.Context) error {
		// A singleton write serializes concurrent bootstrap and administrator mutations.
		if _, e := s.C("counters").UpdateOne(c, bson.M{"_id": "adminGuard"}, bson.M{"$inc": bson.M{"value": 1}}, options.UpdateOne().SetUpsert(true)); e != nil {
			return e
		}
		var marker struct {
			SystemID int64 `bson:"systemId"`
		}
		e := s.C("settings").FindOne(c, bson.M{"_id": "bootstrap"}).Decode(&marker)
		if errors.Is(e, mongo.ErrNoDocuments) {
			now := time.Now().UTC()
			sid, e := s.NextID(c)
			if e != nil {
				return e
			}
			uid, e := s.NextID(c)
			if e != nil {
				return e
			}
			if _, e = s.C("users").InsertMany(c, []any{User{ID: sid, Name: "system", Email: "system@internal.invalid", Role: "USER", Internal: true, CreatedAt: now, UpdatedAt: now}, User{ID: uid, Name: name, Email: email, Role: "ADMIN", PasswordHash: hash, CreatedAt: now, UpdatedAt: now}}); e != nil {
				return e
			}
			if _, e = s.C("settings").InsertOne(c, bson.M{"_id": "bootstrap", "systemId": sid}); e != nil {
				return e
			}
			s.SystemID = sid
		} else if e != nil {
			return e
		} else {
			s.SystemID = marker.SystemID
		}
		for _, image := range images {
			var old OS
			e := s.C("os").FindOne(c, bson.M{"type": image.Type}).Decode(&old)
			if errors.Is(e, mongo.ErrNoDocuments) {
				image.ID, e = s.NextID(c)
				if e != nil {
					return e
				}
			} else if e != nil {
				return e
			} else {
				image.ID = old.ID
			}
			if _, e = s.C("os").ReplaceOne(c, bson.M{"type": image.Type}, image, options.Replace().SetUpsert(true)); e != nil {
				return e
			}
		}
		// Removed registrations are not returned, but retained as historical desktop snapshots.
		keep := []string{}
		for _, im := range images {
			keep = append(keep, im.Type)
		}
		_, e = s.C("os").DeleteMany(c, bson.M{"type": bson.M{"$nin": keep}})
		return e
	})
	return e
}
