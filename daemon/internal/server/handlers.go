package server

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"time"

	"github.com/johnny/dualsub-next/daemon/internal/config"
	"github.com/johnny/dualsub-next/daemon/internal/provider"
	"github.com/johnny/dualsub-next/daemon/internal/translate"
)

// ─── /healthz ───────────────────────────────────────────────────────────────

func handleHealthz(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{
		"status":      "ok",
		"time":        time.Now().UTC().Format(time.RFC3339),
		"install_dir": installDir(),
	})
}

// installDir returns the directory that holds the dualsub binary, which is the
// same directory as the dualsub-watch.sh / dualsub-watch.ps1 helper scripts.
// Returns "" if the path cannot be resolved.
func installDir() string {
	exe, err := os.Executable()
	if err != nil {
		return ""
	}
	if resolved, err := filepath.EvalSymlinks(exe); err == nil {
		exe = resolved
	}
	return filepath.Dir(exe)
}

// ─── /v1/providers ──────────────────────────────────────────────────────────

type providerInfo struct {
	Name         string `json:"name"`
	DefaultModel string `json:"default_model,omitempty"`
}

func (s *Server) handleProviders(w http.ResponseWriter, _ *http.Request) {
	out := make([]providerInfo, 0, len(s.providers))
	for name, p := range s.providers {
		info := providerInfo{Name: name, DefaultModel: p.DefaultModel()}
		out = append(out, info)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	writeJSON(w, http.StatusOK, out)
}

// Body size limits — localhost-only, but cap to avoid trivial OOM from a
// runaway request. A full transcript is typically a few hundred KB; 10 MB is
// generous. Config payloads are tiny.
const (
	maxTranslateBody = 10 << 20 // 10 MB
	maxConfigBody    = 1 << 20  // 1 MB
)

// ─── /v1/translate (SSE) ────────────────────────────────────────────────────

type translateRequest struct {
	Site       string          `json:"site"`
	VideoKey   string          `json:"video_key"`
	Title      string          `json:"title"`
	Provider   string          `json:"provider"`
	Model      string          `json:"model,omitempty"`
	SourceLang string          `json:"source_lang"`
	TargetLang string          `json:"target_lang"`
	Lines      []provider.Line `json:"lines"`
}

func (s *Server) handleTranslate(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "POST only", http.StatusMethodNotAllowed)
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxTranslateBody)
	var req translateRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid json: "+err.Error(), http.StatusBadRequest)
		return
	}
	if req.Provider == "" || len(req.Lines) == 0 {
		http.Error(w, "provider and lines are required", http.StatusBadRequest)
		return
	}
	if _, ok := s.providers[req.Provider]; !ok {
		http.Error(w, "unknown provider: "+req.Provider, http.StatusBadRequest)
		return
	}

	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming not supported by transport", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no") // disable proxy buffering, harmless on direct
	w.WriteHeader(http.StatusOK)

	events := make(chan translate.Event, 32)
	go func() {
		_ = s.orch.Translate(r.Context(), translate.Input{
			VideoKey:   req.VideoKey,
			Title:      req.Title,
			Site:       req.Site,
			Provider:   req.Provider,
			Model:      req.Model,
			SourceLang: req.SourceLang,
			TargetLang: req.TargetLang,
			Lines:      req.Lines,
		}, events)
	}()

	if s.log != nil {
		s.log.Event("translate_start", map[string]any{
			"video_key": req.VideoKey, "provider": req.Provider, "lines": len(req.Lines),
		})
	}
	for ev := range events {
		if err := writeSSE(w, string(ev.Type), ev.Payload); err != nil {
			// Client disconnected. r.Context() will be cancelled, so the
			// orchestrator winds down — but in-flight workers may still send
			// to the buffered channel before they notice. Drain to keep them
			// from blocking and leaking the goroutine.
			for range events {
			}
			return
		}
		flusher.Flush()
	}
}

// ─── /v1/jobs ───────────────────────────────────────────────────────────────

type jobSummary struct {
	JobID           string `json:"job_id"`
	VideoKey        string `json:"video_key"`
	Provider        string `json:"provider"`
	Model           string `json:"model"`
	Status          string `json:"status"`
	TotalChunks     int    `json:"total_chunks"`
	CompletedChunks int    `json:"completed_chunks"`
	FailedChunks    int    `json:"failed_chunks"`
	ErrorSummary    string `json:"error_summary"`
	CreatedAt       int64  `json:"created_at"`
	CompletedAt     *int64 `json:"completed_at"`
}

func (s *Server) handleJobs(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodDelete {
		if err := s.cache.ClearJobs(r.Context()); err != nil {
			http.Error(w, "clear jobs: "+err.Error(), http.StatusInternalServerError)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"ok": true})
		return
	}
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	limit := 10
	if v := r.URL.Query().Get("limit"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 && n <= 200 {
			limit = n
		}
	}
	jobs, err := s.cache.ListJobs(r.Context(), limit)
	if err != nil {
		http.Error(w, "list jobs: "+err.Error(), http.StatusInternalServerError)
		return
	}
	out := make([]jobSummary, len(jobs))
	for i, j := range jobs {
		var completed *int64
		if j.CompletedAt != nil {
			t := j.CompletedAt.Unix()
			completed = &t
		}
		out[i] = jobSummary{
			JobID: j.ID, VideoKey: j.VideoKey, Provider: j.Provider, Model: j.Model,
			Status: j.Status, TotalChunks: j.TotalChunks,
			CompletedChunks: j.CompletedChunks, FailedChunks: j.FailedChunks,
			ErrorSummary: j.ErrorSummary,
			CreatedAt:    j.CreatedAt.Unix(), CompletedAt: completed,
		}
	}
	writeJSON(w, http.StatusOK, out)
}

// ─── /v1/config ─────────────────────────────────────────────────────────────

func (s *Server) handleConfig(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		writeJSON(w, http.StatusOK, s.cfg)
	case http.MethodPut:
		if s.cfgPath == "" {
			http.Error(w, "config path not configured", http.StatusInternalServerError)
			return
		}
		r.Body = http.MaxBytesReader(w, r.Body, maxConfigBody)
		var newCfg config.Config
		if err := json.NewDecoder(r.Body).Decode(&newCfg); err != nil {
			http.Error(w, "invalid json: "+err.Error(), http.StatusBadRequest)
			return
		}
		// GET /v1/config never exposes this secret. Preserve only a token
		// already stored on disk; an environment-provided token must not be
		// copied into config.toml by an unrelated Options-page save.
		if storedCfg, err := config.Load(s.cfgPath); err == nil {
			newCfg.Sync.Token = storedCfg.Sync.Token
		}
		if err := newCfg.Save(s.cfgPath); err != nil {
			http.Error(w, "save config: "+err.Error(), http.StatusInternalServerError)
			return
		}
		if s.log != nil {
			s.log.Event("config_saved", map[string]any{"path": s.cfgPath})
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"ok":      true,
			"message": "Config saved. Restart the daemon to apply changes.",
		})
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}
