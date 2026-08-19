package sharedcache

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/johnny/dualsub-next/daemon/internal/cache"
	"github.com/johnny/dualsub-next/daemon/internal/provider"
)

const (
	maxResolveBody   = 1 << 20
	maxImportBody    = 8 << 20
	maxResolveLines  = 200
	maxImportEntries = 2000
)

type ServerOptions struct {
	Addr      string
	Token     string
	Cache     *cache.Cache
	Providers map[string]provider.Provider
}

type Server struct {
	http     *http.Server
	token    string
	resolver *resolver
	cache    *cache.Cache
}

func NewServer(opts ServerOptions) *Server {
	s := &Server{
		token: opts.Token,
		cache: opts.Cache,
		resolver: &resolver{
			cache: opts.Cache, providers: opts.Providers, inflight: make(map[string]*flightCall),
		},
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", s.auth(s.handleHealth))
	mux.HandleFunc("/v1/resolve", s.auth(s.handleResolve))
	mux.HandleFunc("/v1/import", s.auth(s.handleImport))
	s.http = &http.Server{
		Addr: opts.Addr, Handler: mux, ReadHeaderTimeout: 5 * time.Second,
	}
	return s
}

func (s *Server) ListenAndServe() error              { return s.http.ListenAndServe() }
func (s *Server) Shutdown(ctx context.Context) error { return s.http.Shutdown(ctx) }

func (s *Server) auth(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		provided := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
		if len(provided) != len(s.token) || subtle.ConstantTimeCompare([]byte(provided), []byte(s.token)) != 1 {
			w.Header().Set("WWW-Authenticate", "Bearer")
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		next(w, r)
	}
}

func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func (s *Server) handleResolve(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxResolveBody)
	var req resolveRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid json: "+err.Error(), http.StatusBadRequest)
		return
	}
	translations, hits, err := s.resolver.resolve(r.Context(), req)
	if err != nil {
		status := http.StatusBadGateway
		var bad *requestError
		if errors.As(err, &bad) {
			status = http.StatusBadRequest
		} else if errors.Is(err, context.DeadlineExceeded) {
			status = http.StatusGatewayTimeout
		}
		http.Error(w, err.Error(), status)
		return
	}
	model := req.Model
	if model == "" {
		if prov, ok := s.resolver.providers[req.Provider]; ok {
			model = prov.DefaultModel()
		}
	}
	// Include legacy aliases so clients that still derive provider-specific
	// keys can consume the provider-independent result during rollout.
	for _, line := range req.Lines {
		key := cache.Key(req.Provider, model, req.SourceLang, req.TargetLang, line.Text)
		if translated, ok := translations[key]; ok {
			legacy := cache.LegacyKey(req.Provider, model, req.SourceLang, req.TargetLang, line.Text)
			translations[legacy] = translated
		}
	}
	writeJSON(w, http.StatusOK, resolveResponse{Translations: translations, CacheHits: hits})
}

func (s *Server) handleImport(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxImportBody)
	var req importRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid json: "+err.Error(), http.StatusBadRequest)
		return
	}
	if len(req.Entries) == 0 || len(req.Entries) > maxImportEntries {
		http.Error(w, "entries must contain between 1 and 2000 translations", http.StatusBadRequest)
		return
	}
	keys := make([]string, 0, len(req.Entries))
	normalized := make([]cache.TranslationEntry, len(req.Entries))
	for i, entry := range req.Entries {
		if entry.SourceLang == "" || entry.TargetLang == "" || entry.OriginalText == "" || entry.TranslatedText == "" {
			http.Error(w, "translation entry is missing required fields", http.StatusBadRequest)
			return
		}
		expected := cache.Key(entry.Provider, entry.Model, entry.SourceLang, entry.TargetLang, entry.OriginalText)
		legacy := cache.LegacyKey(entry.Provider, entry.Model, entry.SourceLang, entry.TargetLang, entry.OriginalText)
		if entry.Key != expected && entry.Key != legacy {
			http.Error(w, "translation entry has an invalid cache key", http.StatusBadRequest)
			return
		}
		keys = append(keys, entry.Key)
		entry.Key = expected
		normalized[i] = entry
	}
	if err := s.cache.StoreTranslations(r.Context(), normalized); err != nil {
		http.Error(w, "store translations: "+err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, importResponse{Acknowledged: keys})
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

type requestError struct{ message string }

func (e *requestError) Error() string { return e.message }

type resolver struct {
	cache     *cache.Cache
	providers map[string]provider.Provider
	mu        sync.Mutex
	inflight  map[string]*flightCall
}

type flightCall struct {
	done   chan struct{}
	values map[string]string
	err    error
}

func (r *resolver) resolve(ctx context.Context, req resolveRequest) (map[string]string, int, error) {
	if req.Provider == "" || req.SourceLang == "" || req.TargetLang == "" || len(req.Lines) == 0 {
		return nil, 0, &requestError{"provider, languages, and lines are required"}
	}
	if len(req.Lines) > maxResolveLines {
		return nil, 0, &requestError{"too many lines in one resolve request"}
	}
	model := req.Model
	seenIndexes := make(map[int]struct{}, len(req.Lines))
	keys := make([]string, 0, len(req.Lines))
	lineByKey := make(map[string]provider.Line, len(req.Lines))
	for _, line := range req.Lines {
		if line.Text == "" {
			return nil, 0, &requestError{"line text cannot be empty"}
		}
		if _, exists := seenIndexes[line.Index]; exists {
			return nil, 0, &requestError{"line indexes must be unique"}
		}
		seenIndexes[line.Index] = struct{}{}
		key := cache.Key(req.Provider, model, req.SourceLang, req.TargetLang, line.Text)
		keys = append(keys, key)
		if _, exists := lineByKey[key]; !exists {
			lineByKey[key] = line
		}
	}

	hits, err := r.cache.LookupTranslations(ctx, keys)
	if err != nil {
		return nil, 0, err
	}
	initialHits := len(hits)
	missingKeys := make([]string, 0, len(lineByKey))
	for key := range lineByKey {
		if _, ok := hits[key]; !ok {
			missingKeys = append(missingKeys, key)
		}
	}
	if len(missingKeys) == 0 {
		return hits, initialHits, nil
	}
	prov, ok := r.providers[req.Provider]
	if !ok {
		return nil, initialHits, &requestError{fmt.Sprintf("provider %q is not configured on the shared node", req.Provider)}
	}
	if model == "" {
		model = prov.DefaultModel()
	}
	sort.Strings(missingKeys)

	flightKey := resolveFlightKey(req.Provider, model, req.SourceLang, req.TargetLang, missingKeys)
	resolved, err := r.doFlight(ctx, flightKey, func() (map[string]string, error) {
		// Another overlapping batch may have completed while this request waited.
		current, err := r.cache.LookupTranslations(ctx, missingKeys)
		if err != nil {
			return nil, err
		}
		var lines []provider.Line
		for _, key := range missingKeys {
			if _, ok := current[key]; !ok {
				lines = append(lines, lineByKey[key])
			}
		}
		if len(lines) == 0 {
			return current, nil
		}
		sort.Slice(lines, func(i, j int) bool { return lines[i].Index < lines[j].Index })
		res, err := prov.Translate(ctx, provider.Request{
			Lines: lines, SourceLang: req.SourceLang, TargetLang: req.TargetLang, Model: model,
		})
		if err != nil {
			return nil, err
		}
		byIndex := make(map[int]string, len(res.Lines))
		for _, line := range res.Lines {
			byIndex[line.Index] = line.Text
		}
		entries := make([]cache.TranslationEntry, 0, len(lines))
		for _, line := range lines {
			translated, ok := byIndex[line.Index]
			if !ok || translated == "" {
				return nil, fmt.Errorf("provider omitted translation for line %d", line.Index)
			}
			key := cache.Key(req.Provider, model, req.SourceLang, req.TargetLang, line.Text)
			entries = append(entries, cache.TranslationEntry{
				Key: key, Provider: req.Provider, Model: model, SourceLang: req.SourceLang,
				TargetLang: req.TargetLang, OriginalText: line.Text, TranslatedText: translated,
			})
			current[key] = translated
		}
		if err := r.cache.StoreTranslations(ctx, entries); err != nil {
			return nil, err
		}
		return current, nil
	})
	if err != nil {
		return nil, initialHits, err
	}
	for key, value := range resolved {
		hits[key] = value
	}
	return hits, initialHits, nil
}

func resolveFlightKey(providerName, model, sourceLang, targetLang string, keys []string) string {
	h := sha256.New()
	_, _ = fmt.Fprintf(h, "%s|%s|%s|%s|%s", providerName, model, sourceLang, targetLang, strings.Join(keys, ","))
	return hex.EncodeToString(h.Sum(nil))
}

func (r *resolver) doFlight(ctx context.Context, key string, fn func() (map[string]string, error)) (map[string]string, error) {
	r.mu.Lock()
	if call, ok := r.inflight[key]; ok {
		r.mu.Unlock()
		select {
		case <-call.done:
			return call.values, call.err
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	call := &flightCall{done: make(chan struct{})}
	r.inflight[key] = call
	r.mu.Unlock()

	call.values, call.err = fn()
	r.mu.Lock()
	delete(r.inflight, key)
	close(call.done)
	r.mu.Unlock()
	return call.values, call.err
}
