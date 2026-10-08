package vdi

import (
	"context"
	"errors"
	"github.com/google/uuid"
	"go.mongodb.org/mongo-driver/v2/bson"
	"os"
	"sync"
	"testing"
	"time"
)

func catalogStore(t *testing.T) (*Store, context.Context) {
	t.Helper()
	uri := os.Getenv("VDI_TEST_MONGO_URI")
	if uri == "" {
		t.Skip("VDI_TEST_MONGO_URI must point to a MongoDB replica set")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	t.Cleanup(cancel)
	s, e := OpenStore(ctx, uri, "vdi_catalog_test_"+uuid.NewString())
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() {
		cleanup, stop := context.WithTimeout(context.Background(), 10*time.Second)
		defer stop()
		if e := s.DB.Drop(cleanup); e != nil {
			t.Error(e)
		}
		_ = s.DB.Client().Disconnect(cleanup)
	})
	if e := s.Bootstrap(ctx, "admin", "admin@example.com", "test-password"); e != nil {
		t.Fatal(e)
	}
	return s, ctx
}

func TestCatalogImportOnce(t *testing.T) {
	s, ctx := catalogStore(t)
	load := func(context.Context) ([]OS, error) {
		return []OS{{Name: "Ubuntu", Type: "UBUNTU", Version: "24.04", ImageID: "image-1"}}, nil
	}
	if initialized, e := s.InitializeImages(ctx, load); e != nil || !initialized {
		t.Fatalf("initial import: %v, %v", initialized, e)
	}
	var before OS
	if e := s.C("os").FindOne(ctx, bson.M{}).Decode(&before); e != nil {
		t.Fatal(e)
	}
	if e := s.Bootstrap(ctx, "admin", "admin@example.com", "test-password"); e != nil {
		t.Fatal(e)
	}
	if initialized, e := s.InitializeImages(ctx, func(context.Context) ([]OS, error) {
		t.Error("restart fetched Glance again")
		return nil, errors.New("should not run")
	}); e != nil || initialized {
		t.Fatalf("restart: %v, %v", initialized, e)
	}
	var after OS
	if e := s.C("os").FindOne(ctx, bson.M{}).Decode(&after); e != nil || before != after || after.ID <= 0 {
		t.Fatalf("catalog changed: %+v -> %+v (%v)", before, after, e)
	}
}

func TestCatalogEmptyImportOnce(t *testing.T) {
	s, ctx := catalogStore(t)
	calls := 0
	load := func(context.Context) ([]OS, error) { calls++; return []OS{}, nil }
	for i := 0; i < 2; i++ {
		if _, e := s.InitializeImages(ctx, load); e != nil {
			t.Fatal(e)
		}
	}
	if calls != 1 {
		t.Fatalf("empty successful import called Glance %d times", calls)
	}
}

func TestCatalogFailedImportRetry(t *testing.T) {
	s, ctx := catalogStore(t)
	if _, e := s.InitializeImages(ctx, func(context.Context) ([]OS, error) {
		return nil, errors.New("Glance unavailable")
	}); e == nil {
		t.Fatal("expected failed import")
	}
	if n, e := s.C("settings").CountDocuments(ctx, bson.M{"_id": "os-bootstrap"}); e != nil || n != 0 {
		t.Fatalf("failure marked complete: %d, %v", n, e)
	}
	if initialized, e := s.InitializeImages(ctx, func(context.Context) ([]OS, error) { return []OS{}, nil }); e != nil || !initialized {
		t.Fatalf("retry: %v, %v", initialized, e)
	}
}

func TestCatalogExistingPreserved(t *testing.T) {
	s, ctx := catalogStore(t)
	existing := OS{ID: 100, Name: "Existing", Type: "UBUNTU", Version: "22.04", ImageID: "existing"}
	if _, e := s.C("os").InsertOne(ctx, existing); e != nil {
		t.Fatal(e)
	}
	if _, e := s.InitializeImages(ctx, func(context.Context) ([]OS, error) {
		t.Error("existing catalog fetched Glance")
		return nil, errors.New("should not run")
	}); e != nil {
		t.Fatal(e)
	}
	var after OS
	if e := s.C("os").FindOne(ctx, bson.M{}).Decode(&after); e != nil || after != existing {
		t.Fatalf("existing catalog changed: %+v, %v", after, e)
	}
}

func TestCatalogConcurrentImports(t *testing.T) {
	s, ctx := catalogStore(t)
	const pods = 4
	ready := make(chan struct{}, pods)
	release := make(chan struct{})
	results := make(chan error, pods)
	var workers sync.WaitGroup
	for i := 0; i < pods; i++ {
		workers.Add(1)
		go func() {
			defer workers.Done()
			_, e := s.InitializeImages(ctx, func(context.Context) ([]OS, error) {
				ready <- struct{}{}
				select {
				case <-release:
					return []OS{{Name: "Ubuntu", Type: "UBUNTU", Version: "24.04", ImageID: "image-1"}}, nil
				case <-ctx.Done():
					return nil, ctx.Err()
				}
			})
			results <- e
		}()
	}
	for i := 0; i < pods; i++ {
		select {
		case <-ready:
		case <-ctx.Done():
			close(release)
			workers.Wait()
			t.Fatal("concurrent imports did not reach Glance")
		}
	}
	close(release)
	workers.Wait()
	for i := 0; i < pods; i++ {
		if e := <-results; e != nil {
			t.Fatal(e)
		}
	}
	if n, e := s.C("os").CountDocuments(ctx, bson.M{}); e != nil || n != 1 {
		t.Fatalf("duplicate catalog entries: %d, %v", n, e)
	}
	if n, e := s.C("settings").CountDocuments(ctx, bson.M{"_id": "os-bootstrap"}); e != nil || n != 1 {
		t.Fatalf("duplicate completion markers: %d, %v", n, e)
	}
}
