package sharedcache

import (
	"github.com/johnny/dualsub-next/daemon/internal/cache"
	"github.com/johnny/dualsub-next/daemon/internal/provider"
)

type resolveRequest struct {
	Provider   string          `json:"provider"`
	Model      string          `json:"model,omitempty"`
	SourceLang string          `json:"source_lang"`
	TargetLang string          `json:"target_lang"`
	Lines      []provider.Line `json:"lines"`
}

type resolveResponse struct {
	Translations map[string]string `json:"translations"`
	CacheHits    int               `json:"cache_hits"`
}

type importRequest struct {
	Entries []cache.TranslationEntry `json:"entries"`
}

type importResponse struct {
	Acknowledged []string `json:"acknowledged"`
}

// lookupRequest asks the central cache for existing translations only; the
// central never translates for it. Keys are provider-independent, so no
// provider or model is carried.
type lookupRequest struct {
	SourceLang string          `json:"source_lang"`
	TargetLang string          `json:"target_lang"`
	Lines      []provider.Line `json:"lines"`
}

type lookupResponse struct {
	Translations map[string]string `json:"translations"`
	CacheHits    int               `json:"cache_hits"`
}
