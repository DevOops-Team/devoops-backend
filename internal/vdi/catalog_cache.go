package vdi

import (
	"context"
	"sync"
	"time"
)

const catalogTTL = 30 * time.Second
const catalogMaxEntries = 128

type specEntry struct {
	spec    Spec
	expires time.Time
}

type specFlight struct {
	done chan struct{}
	spec Spec
	err  error
}

// The cache belongs to one App/cloud identity. Only public catalog metadata is
// cached; authorization, VM state and create-time validation remain live.
type specCache struct {
	mu      sync.Mutex
	entries map[string]specEntry
	flights map[string]*specFlight
}

func (a *App) catalogSpec(ctx context.Context, image string) (Spec, error) {
	return a.catalog.get(ctx, image, a.Now, a.Cloud.Spec)
}

func (c *specCache) get(ctx context.Context, image string, now func() time.Time, load func(context.Context, string) (Spec, error)) (Spec, error) {
	if err := ctx.Err(); err != nil {
		return Spec{}, err
	}
	c.mu.Lock()
	if entry, ok := c.entries[image]; ok && now().Before(entry.expires) {
		c.mu.Unlock()
		return entry.spec, nil
	}
	if flight, ok := c.flights[image]; ok {
		c.mu.Unlock()
		select {
		case <-ctx.Done():
			return Spec{}, ctx.Err()
		case <-flight.done:
			return flight.spec, flight.err
		}
	}
	if c.flights == nil {
		c.flights = make(map[string]*specFlight)
	}
	flight := &specFlight{done: make(chan struct{})}
	c.flights[image] = flight
	c.mu.Unlock()

	spec, err := load(ctx, image)
	c.mu.Lock()
	defer c.mu.Unlock()
	// Failures are never retained. After expiry an upstream failure is returned
	// rather than presenting old metadata as current availability.
	delete(c.entries, image)
	if err == nil {
		if c.entries == nil {
			c.entries = make(map[string]specEntry)
		}
		t := now()
		for key, entry := range c.entries {
			if !t.Before(entry.expires) {
				delete(c.entries, key)
			}
		}
		if len(c.entries) >= catalogMaxEntries {
			for key := range c.entries {
				delete(c.entries, key)
				break
			}
		}
		c.entries[image] = specEntry{spec: spec, expires: t.Add(catalogTTL)}
	}
	flight.spec, flight.err = spec, err
	delete(c.flights, image)
	close(flight.done)
	return spec, err
}
