package agent

import (
	"context"
	"sync"
	"time"

	"visitready/internal/domain"
)

const defaultSearchCacheTTL = 10 * time.Minute

type cachedSearchResult struct {
	sources   []domain.Source
	expiresAt time.Time
}

type searchCache struct {
	mu      sync.RWMutex
	entries map[string]cachedSearchResult
	ttl     time.Duration
}

func newSearchCache(ttl time.Duration) *searchCache {
	if ttl <= 0 {
		ttl = defaultSearchCacheTTL
	}
	return &searchCache{entries: make(map[string]cachedSearchResult), ttl: ttl}
}

func (c *searchCache) get(key string, now time.Time) ([]domain.Source, bool) {
	c.mu.RLock()
	entry, ok := c.entries[key]
	c.mu.RUnlock()
	if !ok || !entry.expiresAt.After(now) {
		if ok {
			c.mu.Lock()
			delete(c.entries, key)
			c.mu.Unlock()
		}
		return nil, false
	}
	return append([]domain.Source(nil), entry.sources...), true
}

func (c *searchCache) put(key string, sources []domain.Source, now time.Time) {
	c.mu.Lock()
	c.entries[key] = cachedSearchResult{sources: append([]domain.Source(nil), sources...), expiresAt: now.Add(c.ttl)}
	c.mu.Unlock()
}

func (r *Runner) searchWithCache(ctx context.Context, query string) ([]domain.Source, error) {
	if sources, ok := r.searchCache.get(query, r.now()); ok {
		return sources, nil
	}
	sources, err := r.search.Search(ctx, query, append([]string(nil), r.allowedDomains...))
	if err == nil {
		r.searchCache.put(query, sources, r.now())
	}
	return sources, err
}
