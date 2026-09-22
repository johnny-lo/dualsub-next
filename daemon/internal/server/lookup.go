package server

import (
	"encoding/json"
	"errors"
	"net/http"

	"github.com/johnny/dualsub-next/daemon/internal/cache"
	"github.com/johnny/dualsub-next/daemon/internal/provider"
	"github.com/johnny/dualsub-next/daemon/internal/sharedcache"
)

// ─── /v1/lookup ─────────────────────────────────────────────────────────────
// Cache-only: answers which lines already have a translation, locally or on
// the central node. Never calls a provider, never creates a job. The
// extension calls this on every page load, so a miss must be free.

const maxLookupLines = 2000

type lookupRequest struct {
	VideoKey      string          `json:"video_key,omitempty"`
	SourceLang    string          `json:"source_lang"`
	TargetLang    string          `json:"target_lang"`
	Lines         []provider.Line `json:"lines"`
	IncludeRemote bool            `json:"include_remote"`
}

type lookupResponse struct {
	Translations []provider.TranslatedLine `json:"translations"`
	Hits         int                       `json:"hits"`
	Total        int                       `json:"total"`
	RemoteHits   int                       `json:"remote_hits"`
	RemoteStatus string                    `json:"remote_status"` // disabled | ok | unsupported | unavailable
}

func (s *Server) handleLookup(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "POST only", http.StatusMethodNotAllowed)
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxTranslateBody)
	var req lookupRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid json: "+err.Error(), http.StatusBadRequest)
		return
	}
	if req.SourceLang == "" || req.TargetLang == "" || len(req.Lines) == 0 {
		http.Error(w, "source_lang, target_lang, and lines are required", http.StatusBadRequest)
		return
	}
	if len(req.Lines) > maxLookupLines {
		http.Error(w, "too many lines in one lookup request", http.StatusBadRequest)
		return
	}

	ctx := r.Context()
	keys := make([]string, len(req.Lines))
	for i, l := range req.Lines {
		keys[i] = cache.Key("", "", req.SourceLang, req.TargetLang, l.Text)
	}
	hits, err := s.cache.LookupTranslations(ctx, keys)
	if err != nil {
		http.Error(w, "lookup translations: "+err.Error(), http.StatusInternalServerError)
		return
	}

	remoteStatus := "disabled"
	remoteKeys := map[string]struct{}{}
	if req.IncludeRemote && s.remote != nil {
		remoteStatus = "ok"
		missing := missingLines(req.Lines, keys, hits)
		if len(missing) > 0 {
			found, err := s.remote.Lookup(ctx, req.SourceLang, req.TargetLang, missing)
			switch {
			case errors.Is(err, sharedcache.ErrLookupUnsupported):
				remoteStatus = "unsupported"
			case err != nil:
				remoteStatus = "unavailable"
			default:
				entries := make([]cache.TranslationEntry, 0, len(found))
				for _, l := range missing {
					key := cache.Key("", "", req.SourceLang, req.TargetLang, l.Text)
					translated, ok := found[key]
					if !ok || translated == "" {
						continue
					}
					if _, dup := hits[key]; dup {
						continue
					}
					hits[key] = translated
					remoteKeys[key] = struct{}{}
					entries = append(entries, cache.TranslationEntry{
						Key: key, Provider: "central", SourceLang: req.SourceLang, TargetLang: req.TargetLang,
						OriginalText: l.Text, TranslatedText: translated,
					})
				}
				// StoreTranslations does not touch sync_outbox: these rows came
				// from the central, so uploading them back would be a no-op.
				if len(entries) > 0 {
					if err := s.cache.StoreTranslations(ctx, entries); err != nil && s.log != nil {
						s.log.Event("lookup_store_failed", map[string]any{"error": err.Error(), "entries": len(entries)})
					}
				}
			}
		}
	}

	res := lookupResponse{Total: len(req.Lines), RemoteStatus: remoteStatus}
	for i, l := range req.Lines {
		translated, ok := hits[keys[i]]
		if !ok {
			continue
		}
		res.Hits++
		if _, fromRemote := remoteKeys[keys[i]]; fromRemote {
			res.RemoteHits++
		}
		res.Translations = append(res.Translations, provider.TranslatedLine{Index: l.Index, Text: translated})
	}
	if res.Translations == nil {
		res.Translations = []provider.TranslatedLine{}
	}
	if s.log != nil {
		s.log.Event("lookup", map[string]any{
			"video_key": req.VideoKey, "lines": res.Total, "hits": res.Hits,
			"remote_hits": res.RemoteHits, "remote_status": res.RemoteStatus,
		})
	}
	writeJSON(w, http.StatusOK, res)
}

// missingLines returns one line per distinct key that the local cache lacks.
func missingLines(lines []provider.Line, keys []string, hits map[string]string) []provider.Line {
	seen := make(map[string]struct{}, len(lines))
	var missing []provider.Line
	for i, l := range lines {
		if _, ok := hits[keys[i]]; ok {
			continue
		}
		if _, dup := seen[keys[i]]; dup {
			continue
		}
		seen[keys[i]] = struct{}{}
		missing = append(missing, l)
	}
	return missing
}
