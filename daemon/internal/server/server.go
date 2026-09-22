package server

import (
	"context"
	"encoding/json"
	"net/http"
	"time"

	"github.com/johnny/dualsub-next/daemon/internal/cache"
	"github.com/johnny/dualsub-next/daemon/internal/config"
	"github.com/johnny/dualsub-next/daemon/internal/logger"
	"github.com/johnny/dualsub-next/daemon/internal/provider"
	"github.com/johnny/dualsub-next/daemon/internal/translate"
)

// RemoteLookup is the cache-only question the daemon can ask the central node.
type RemoteLookup interface {
	Lookup(ctx context.Context, sourceLang, targetLang string, lines []provider.Line) (map[string]string, error)
}

type Options struct {
	Addr         string
	Orchestrator *translate.Orchestrator
	Providers    map[string]provider.Provider
	Cache        *cache.Cache
	Config       *config.Config
	ConfigPath   string
	Logger       *logger.Logger
	// RemoteLookup asks the central node's cache without translating. Leave
	// nil when no central is configured. *sharedcache.Client satisfies it.
	RemoteLookup RemoteLookup
}

type Server struct {
	http      *http.Server
	orch      *translate.Orchestrator
	providers map[string]provider.Provider
	cache     *cache.Cache
	cfg       *config.Config
	cfgPath   string
	log       *logger.Logger
	remote    RemoteLookup
}

func New(opts Options) *Server {
	s := &Server{
		orch:      opts.Orchestrator,
		providers: opts.Providers,
		cache:     opts.Cache,
		cfg:       opts.Config,
		cfgPath:   opts.ConfigPath,
		log:       opts.Logger,
		remote:    opts.RemoteLookup,
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", handleHealthz)
	mux.HandleFunc("/v1/providers", s.handleProviders)
	mux.HandleFunc("/v1/translate", s.handleTranslate)
	mux.HandleFunc("/v1/jobs", s.handleJobs)
	mux.HandleFunc("/v1/config", s.handleConfig)
	mux.HandleFunc("/v1/lookup", s.handleLookup)

	s.http = &http.Server{
		Addr:              opts.Addr,
		Handler:           withCORS(mux),
		ReadHeaderTimeout: 5 * time.Second,
		// No write timeout: SSE streams can be long-running.
	}
	return s
}

func (s *Server) ListenAndServe() error              { return s.http.ListenAndServe() }
func (s *Server) Shutdown(ctx context.Context) error { return s.http.Shutdown(ctx) }

func writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}

func withCORS(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Access-Control-Allow-Methods", "GET, POST, PUT, DELETE, OPTIONS")
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type, Authorization")
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		next.ServeHTTP(w, r)
	})
}
