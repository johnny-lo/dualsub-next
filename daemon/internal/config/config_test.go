package config

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLoadMissingFileReturnsZero(t *testing.T) {
	cfg, err := Load(filepath.Join(t.TempDir(), "nope.toml"))
	if err != nil {
		t.Fatalf("expected nil err for missing file, got %v", err)
	}
	if cfg == nil {
		t.Fatal("expected zero config, got nil")
	}
}

func TestSaveLoadRoundTrip(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.toml")

	in := &Config{}
	in.Server.Listen = "127.0.0.1:7878"
	in.Translate.ChunkSize = 25
	in.Translate.Concurrency = 4
	in.Translate.MaxAttempts = 5
	in.Cache.Path = "/tmp/test.db"
	in.Sync = SyncConfig{
		CentralURL: "http://ubuntu-dev:7879", Token: "sync-secret", TokenFile: "/tmp/sync.token",
		ConnectTimeoutMS: 500, RequestTimeoutSeconds: 120, IntervalSeconds: 20,
	}
	in.Providers.Gemini = &GeminiProvider{
		APIKey: "abc", BaseURL: "https://example.com", DefaultModel: "gemini-2.5-flash",
	}
	in.Providers.Ollama = &OllamaProvider{BaseURL: "http://localhost:11434"}

	if err := in.Save(path); err != nil {
		t.Fatalf("save: %v", err)
	}

	out, err := Load(path)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if out.Server.Listen != in.Server.Listen {
		t.Errorf("listen lost: %q vs %q", out.Server.Listen, in.Server.Listen)
	}
	if out.Translate.ChunkSize != 25 || out.Translate.Concurrency != 4 || out.Translate.MaxAttempts != 5 {
		t.Errorf("translate fields lost: %+v", out.Translate)
	}
	if out.Providers.Gemini == nil || out.Providers.Gemini.APIKey != "abc" {
		t.Errorf("gemini config lost: %+v", out.Providers.Gemini)
	}
	if out.Providers.Ollama == nil || out.Providers.Ollama.BaseURL != "http://localhost:11434" {
		t.Errorf("ollama config lost: %+v", out.Providers.Ollama)
	}
	if out.Sync != in.Sync {
		t.Errorf("sync config lost: %+v vs %+v", out.Sync, in.Sync)
	}
}

func TestEnabledProviders(t *testing.T) {
	c := &Config{}
	if got := c.EnabledProviders(); len(got) != 0 {
		t.Errorf("empty config should yield no providers, got %v", got)
	}

	c.Providers.OpenAI = &OpenAIProvider{} // empty key
	c.Providers.Gemini = &GeminiProvider{APIKey: "k"}
	c.Providers.Ollama = &OllamaProvider{}

	got := c.EnabledProviders()
	if len(got) != 2 {
		t.Fatalf("expected 2 enabled, got %v", got)
	}
	// Order is registration order: gemini then ollama (openai skipped because no key)
	if got[0] != "gemini" || got[1] != "ollama" {
		t.Errorf("unexpected order: %v", got)
	}
}

func TestCodexConfigRoundTripAndEnabled(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.toml")

	in := &Config{}
	in.Providers.Codex = &CodexProvider{
		Bin: "codex", Profile: "work", Model: "gpt-5", Sandbox: "read-only",
	}
	if err := in.Save(path); err != nil {
		t.Fatalf("save: %v", err)
	}
	out, err := Load(path)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if out.Providers.Codex == nil || out.Providers.Codex.Profile != "work" || out.Providers.Codex.Model != "gpt-5" {
		t.Errorf("codex config lost: %+v", out.Providers.Codex)
	}

	// EnabledProviders: present block → enabled, no key required (like ollama).
	c := &Config{}
	c.Providers.Codex = &CodexProvider{}
	got := c.EnabledProviders()
	if len(got) != 1 || got[0] != "codex" {
		t.Errorf("expected [codex], got %v", got)
	}
}

func TestApplyEnvOverrides(t *testing.T) {
	t.Setenv("OPENAI_API_KEY", "env-openai")
	t.Setenv("GEMINI_API_KEY", "env-gemini")
	t.Setenv("DUALSUB_SYNC_TOKEN", "env-sync")

	c := &Config{}
	c.ApplyEnvOverrides()

	if c.Providers.OpenAI == nil || c.Providers.OpenAI.APIKey != "env-openai" {
		t.Errorf("openai key not applied: %+v", c.Providers.OpenAI)
	}
	if c.Providers.Gemini == nil || c.Providers.Gemini.APIKey != "env-gemini" {
		t.Errorf("gemini key not applied: %+v", c.Providers.Gemini)
	}
	if c.Sync.Token != "env-sync" {
		t.Errorf("sync token not applied: %q", c.Sync.Token)
	}
}

func TestDefaultsAndExpand(t *testing.T) {
	c := &Config{}
	c.Cache.Path = "~/test/cache.db"
	c.Defaults()
	if c.Server.Listen != "127.0.0.1:7878" {
		t.Errorf("listen default lost: %q", c.Server.Listen)
	}
	if c.Translate.ChunkSize != 30 {
		t.Errorf("chunk_size default lost: %d", c.Translate.ChunkSize)
	}
	if c.Sync.ConnectTimeoutMS != 800 || c.Sync.RequestTimeoutSeconds != 360 || c.Sync.IntervalSeconds != 30 {
		t.Errorf("sync defaults lost: %+v", c.Sync)
	}
	home, _ := os.UserHomeDir()
	if c.Cache.Path != filepath.Join(home, "test/cache.db") {
		t.Errorf("expand ~ failed: %q", c.Cache.Path)
	}
}

func TestSyncTokenOmittedFromJSON(t *testing.T) {
	data, err := json.Marshal(Config{Sync: SyncConfig{CentralURL: "http://central:7879", Token: "secret"}})
	if err != nil {
		t.Fatal(err)
	}
	if string(data) == "" || strings.Contains(string(data), "secret") || strings.Contains(string(data), `"token":`) {
		t.Fatalf("sync token leaked in JSON: %s", data)
	}
}

func TestLoadSyncTokenFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sync.token")
	if err := os.WriteFile(path, []byte("  file-secret\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	c := &Config{Sync: SyncConfig{TokenFile: path}}
	if err := c.LoadSyncToken(); err != nil {
		t.Fatal(err)
	}
	if c.Sync.Token != "file-secret" {
		t.Fatalf("token = %q", c.Sync.Token)
	}
}
