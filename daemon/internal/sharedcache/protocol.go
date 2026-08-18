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
