package server

import (
	"errors"
	"net/http"
	"sync"
	"time"

	"github.com/johnny/dualsub-next/daemon/internal/provider"
)

// modelCacheTTL bounds how stale a listed model catalog may be; the popup's
// refresh button bypasses it with ?refresh=1.
const modelCacheTTL = 5 * time.Minute

// modelCache remembers each provider's last successful model listing.
// Failures are never stored, so the next request retries.
type modelCache struct {
	mu      sync.Mutex
	ttl     time.Duration
	now     func() time.Time
	entries map[string]modelCacheEntry
}

type modelCacheEntry struct {
	models    []provider.ModelInfo
	fetchedAt time.Time
}

func newModelCache() *modelCache {
	return &modelCache{ttl: modelCacheTTL, now: time.Now, entries: map[string]modelCacheEntry{}}
}

func (c *modelCache) get(name string) (modelCacheEntry, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	e, ok := c.entries[name]
	if !ok || c.now().Sub(e.fetchedAt) >= c.ttl {
		return modelCacheEntry{}, false
	}
	return e, true
}

func (c *modelCache) put(name string, models []provider.ModelInfo) modelCacheEntry {
	c.mu.Lock()
	defer c.mu.Unlock()
	e := modelCacheEntry{models: models, fetchedAt: c.now()}
	c.entries[name] = e
	return e
}

type modelsResponse struct {
	Models    []provider.ModelInfo `json:"models"`
	FetchedAt string               `json:"fetched_at"`
}

// handleProviderModels serves GET /v1/providers/{name}/models[?refresh=1]:
// the models the provider's account can use now, as its CLI or API reports.
func (s *Server) handleProviderModels(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	p, ok := s.providers[name]
	if !ok {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "unknown provider: " + name})
		return
	}
	lister, ok := p.(provider.ModelLister)
	if !ok {
		writeJSON(w, http.StatusNotImplemented, map[string]string{"error": provider.ErrListUnsupported.Error()})
		return
	}

	entry, cached := modelCacheEntry{}, false
	if r.URL.Query().Get("refresh") != "1" {
		entry, cached = s.models.get(name)
	}
	if !cached {
		// The lock is not held here: a CLI listing takes seconds, and two
		// concurrent cold requests running it twice is harmless.
		models, err := lister.ListModels(r.Context())
		if errors.Is(err, provider.ErrListUnsupported) {
			writeJSON(w, http.StatusNotImplemented, map[string]string{"error": err.Error()})
			return
		}
		if err != nil {
			writeJSON(w, http.StatusBadGateway, map[string]string{"error": err.Error()})
			return
		}
		if models == nil {
			models = []provider.ModelInfo{}
		}
		entry = s.models.put(name, models)
	}
	writeJSON(w, http.StatusOK, modelsResponse{
		Models:    entry.models,
		FetchedAt: entry.fetchedAt.UTC().Format(time.RFC3339),
	})
}
