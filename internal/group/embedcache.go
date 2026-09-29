package group

import (
	"context"
	"sync"

	"github.com/anatolykoptev/go-kit/embed"
)

// embedCacheLRU is a process-local FIFO cache behind the embed.Cache
// interface, keyed by the deterministic key go-kit computes (model + dim
// + role + text hash). Product names recur across searches and watch
// checks, so repeat embeddings short the HTTP call. 2048 entries × ~4KB
// (1024-dim float32) ≈ 8MB — negligible next to the scrape pipeline.
// Mirrors the go-search embed cache; Redis L2 is deliberately skipped —
// a network round-trip on every embed miss costs more than it saves.
type embedCacheLRU struct {
	mu      sync.Mutex
	items   map[string][]float32
	order   []string
	maxSize int
}

// NewEmbedCache returns an embed.Cache bounded to maxSize entries.
func NewEmbedCache(maxSize int) embed.Cache {
	if maxSize <= 0 {
		maxSize = 2048
	}
	return &embedCacheLRU{items: make(map[string][]float32, maxSize), order: make([]string, 0, maxSize), maxSize: maxSize}
}

// Get implements embed.Cache.
func (c *embedCacheLRU) Get(_ context.Context, key string) ([]float32, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	v, ok := c.items[key]
	return v, ok
}

// Set implements embed.Cache.
func (c *embedCacheLRU) Set(_ context.Context, key string, vector []float32) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if _, exists := c.items[key]; exists {
		return
	}
	if len(c.order) >= c.maxSize {
		evict := c.order[0]
		c.order = c.order[1:]
		delete(c.items, evict)
	}
	c.order = append(c.order, key)
	c.items[key] = append([]float32(nil), vector...)
}
