package vdi

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestCatalogCacheExpiryAndFailure(t *testing.T) {
	var cache specCache
	now := time.Now()
	clock := func() time.Time { return now }
	calls := 0
	fail := false
	load := func(ctx context.Context, image string) (Spec, error) {
		calls++
		if fail {
			return Spec{}, ErrCloudUnavailable
		}
		return Spec{FlavorID: image, CPUCores: calls}, nil
	}
	get := func(image string) (Spec, error) { return cache.get(context.Background(), image, clock, load) }
	first, _ := get("ubuntu")
	second, _ := get("ubuntu")
	if first != second || calls != 1 {
		t.Fatal("cache miss", first, second, calls)
	}
	other, _ := get("windows")
	if other.FlavorID != "windows" || calls != 2 {
		t.Fatal("image keys mixed")
	}
	now = now.Add(catalogTTL)
	fail = true
	if _, err := get("ubuntu"); !errors.Is(err, ErrCloudUnavailable) {
		t.Fatal("stale success returned", err)
	}
	if _, err := get("ubuntu"); !errors.Is(err, ErrCloudUnavailable) || calls != 4 {
		t.Fatal("failure cached", calls, err)
	}
	fail = false
	if spec, err := get("ubuntu"); err != nil || spec.CPUCores != 5 {
		t.Fatal(spec, err)
	}
}

func TestCatalogCacheConcurrentRequests(t *testing.T) {
	var cache specCache
	var calls atomic.Int32
	started, release := make(chan struct{}), make(chan struct{})
	load := func(context.Context, string) (Spec, error) {
		if calls.Add(1) == 1 {
			close(started)
		}
		<-release
		return Spec{CPUCores: 2}, nil
	}
	var wg sync.WaitGroup
	for range 32 {
		wg.Go(func() {
			spec, err := cache.get(context.Background(), "ubuntu", time.Now, load)
			if err != nil || spec.CPUCores != 2 {
				t.Errorf("%v %v", spec, err)
			}
		})
	}
	<-started
	close(release)
	wg.Wait()
	if calls.Load() != 1 {
		t.Fatal("duplicate upstream calls", calls.Load())
	}
}

func TestCatalogCacheWaiterCancellation(t *testing.T) {
	var cache specCache
	started, release, finished := make(chan struct{}), make(chan struct{}), make(chan struct{})
	load := func(context.Context, string) (Spec, error) {
		close(started)
		<-release
		return Spec{CPUCores: 2}, nil
	}
	go func() { defer close(finished); cache.get(context.Background(), "ubuntu", time.Now, load) }()
	<-started
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()
	if _, err := cache.get(ctx, "ubuntu", time.Now, load); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatal(err)
	}
	close(release)
	<-finished
	if spec, err := cache.get(context.Background(), "ubuntu", time.Now, load); err != nil || spec.CPUCores != 2 {
		t.Fatal(spec, err)
	}
}

func TestCatalogCacheBoundedAndIsolated(t *testing.T) {
	var first, second specCache
	load := func(context.Context, string) (Spec, error) { return Spec{CPUCores: 1}, nil }
	for i := range catalogMaxEntries * 2 {
		first.get(context.Background(), fmt.Sprint(i), time.Now, load)
	}
	if len(first.entries) > catalogMaxEntries {
		t.Fatal("unbounded cache")
	}
	other := func(context.Context, string) (Spec, error) { return Spec{CPUCores: 4}, nil }
	if spec, _ := second.get(context.Background(), "0", time.Now, other); spec.CPUCores != 4 {
		t.Fatal("cross-app cache")
	}
}
