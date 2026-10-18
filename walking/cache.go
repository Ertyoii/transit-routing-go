package walking

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
)

// Cache is a bounded FIFO route cache. It preserves the provider snapshot and
// request policies; errors and partial responses are never retained. It does not
// coalesce simultaneous misses or expose an invented matrix implementation.
type Cache struct {
	provider Provider
	max      int
	mu       sync.Mutex
	entries  map[string]Result
	order    []string
}

func NewCache(p Provider, maxEntries int) (*Cache, error) {
	if p == nil || maxEntries <= 0 {
		return nil, ErrInvalid
	}
	return &Cache{provider: p, max: maxEntries, entries: map[string]Result{}}, nil
}
func (c *Cache) Descriptor() Descriptor { return c.provider.Descriptor() }
func (c *Cache) Route(ctx context.Context, r Request) (Result, error) {
	if e := ctx.Err(); e != nil {
		return Result{}, e
	}
	if r.SnapshotID != c.provider.Descriptor().SnapshotID {
		return Result{}, ErrStale
	}
	if e := r.Options.Validate(); e != nil {
		return Result{}, e
	}
	b, e := json.Marshal(r)
	if e != nil {
		return Result{}, fmt.Errorf("%w: request encoding", ErrInvalid)
	}
	key := string(b)
	c.mu.Lock()
	v, ok := c.entries[key]
	if ok {
		v = CloneResult(v)
	}
	c.mu.Unlock()
	if ok {
		if e := ctx.Err(); e != nil {
			return Result{}, e
		}
		if r.SnapshotID != c.provider.Descriptor().SnapshotID {
			return Result{}, ErrStale
		}
		return v, nil
	}
	v, e = c.provider.Route(ctx, r)
	if e != nil {
		return Result{}, e
	}
	if e = ctx.Err(); e != nil {
		return Result{}, e
	}
	if r.SnapshotID != c.provider.Descriptor().SnapshotID {
		return Result{}, ErrStale
	}
	if e = CheckResult(r, v); e != nil {
		return Result{}, e
	}
	c.mu.Lock()
	if _, exists := c.entries[key]; !exists {
		if len(c.order) == c.max {
			delete(c.entries, c.order[0])
			c.order = c.order[1:]
		}
		c.order = append(c.order, key)
		c.entries[key] = CloneResult(v)
	}
	c.mu.Unlock()
	return CloneResult(v), nil
}
