// Package config loads daemon configuration from a TOML file with env-var
// overrides for secrets. The default lookup path is ~/.config/dualsub/config.toml.
package config

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/BurntSushi/toml"
)

type Config struct {
	Server    ServerConfig    `toml:"server"    json:"server"`
	Translate TranslateConfig `toml:"translate" json:"translate"`
	Cache     CacheConfig     `toml:"cache"     json:"cache"`
	Sync      SyncConfig      `toml:"sync"      json:"sync"`
	Providers ProvidersConfig `toml:"providers" json:"providers"`
}

type ServerConfig struct {
	Listen string `toml:"listen" json:"listen"`
}

type TranslateConfig struct {
	ChunkSize   int `toml:"chunk_size"   json:"chunk_size"`
	Concurrency int `toml:"concurrency"  json:"concurrency"`
	MaxAttempts int `toml:"max_attempts" json:"max_attempts"`
}

type CacheConfig struct {
	Path string `toml:"path" json:"path"`
}

// SyncConfig enables local-first sharing without exposing the browser-facing
// daemon API to the tailnet. A central node sets Listen; client nodes set
// CentralURL. Token is deliberately omitted from JSON config responses.
type SyncConfig struct {
	Listen                string `toml:"listen"                  json:"listen"`
	CentralURL            string `toml:"central_url"             json:"central_url"`
	Token                 string `toml:"token"                   json:"-"`
	TokenFile             string `toml:"token_file"              json:"token_file"`
	ConnectTimeoutMS      int    `toml:"connect_timeout_ms"      json:"connect_timeout_ms"`
	RequestTimeoutSeconds int    `toml:"request_timeout_seconds" json:"request_timeout_seconds"`
	IntervalSeconds       int    `toml:"interval_seconds"        json:"interval_seconds"`
}

type ProvidersConfig struct {
	OpenAI *OpenAIProvider `toml:"openai" json:"openai,omitempty"`
	Gemini *GeminiProvider `toml:"gemini" json:"gemini,omitempty"`
	Ollama *OllamaProvider `toml:"ollama" json:"ollama,omitempty"`
	Codex  *CodexProvider  `toml:"codex"  json:"codex,omitempty"`
}

type OpenAIProvider struct {
	APIKey       string `toml:"api_key"       json:"api_key"`
	BaseURL      string `toml:"base_url"      json:"base_url"`
	DefaultModel string `toml:"default_model" json:"default_model"`
}

type GeminiProvider struct {
	APIKey       string `toml:"api_key"       json:"api_key"`
	BaseURL      string `toml:"base_url"      json:"base_url"`
	DefaultModel string `toml:"default_model" json:"default_model"`
}

type OllamaProvider struct {
	BaseURL      string `toml:"base_url"      json:"base_url"`
	DefaultModel string `toml:"default_model" json:"default_model"`
}

type CodexProvider struct {
	Bin     string `toml:"bin"     json:"bin"`
	Profile string `toml:"profile" json:"profile"`
	Model   string `toml:"model"   json:"model"`
	Sandbox string `toml:"sandbox" json:"sandbox"`
}

// DefaultPath returns ~/.config/dualsub/config.toml.
func DefaultPath() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return "config.toml"
	}
	return filepath.Join(home, ".config", "dualsub", "config.toml")
}

// Load reads the TOML file at path. Returns a zero-value Config (no error)
// when path does not exist, so the caller can still rely on env-var fallbacks.
func Load(path string) (*Config, error) {
	cfg := &Config{}
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return cfg, nil
		}
		return nil, fmt.Errorf("read %s: %w", path, err)
	}
	if err := toml.Unmarshal(data, cfg); err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}
	return cfg, nil
}

// ApplyEnvOverrides lets the user override secrets via environment variables
// without editing the config file. Empty env values do not overwrite.
func (c *Config) ApplyEnvOverrides() {
	setIfEmpty := func(s *string, env string) {
		if v := os.Getenv(env); v != "" {
			*s = v
		}
	}

	if v := os.Getenv("OPENAI_API_KEY"); v != "" {
		if c.Providers.OpenAI == nil {
			c.Providers.OpenAI = &OpenAIProvider{}
		}
		c.Providers.OpenAI.APIKey = v
	}
	if v := os.Getenv("GEMINI_API_KEY"); v != "" {
		if c.Providers.Gemini == nil {
			c.Providers.Gemini = &GeminiProvider{}
		}
		c.Providers.Gemini.APIKey = v
	}
	if v := os.Getenv("OLLAMA_BASE_URL"); v != "" {
		if c.Providers.Ollama == nil {
			c.Providers.Ollama = &OllamaProvider{}
		}
		c.Providers.Ollama.BaseURL = v
	}

	if c.Server.Listen == "" {
		setIfEmpty(&c.Server.Listen, "DUALSUB_LISTEN")
	}
	setIfEmpty(&c.Sync.Token, "DUALSUB_SYNC_TOKEN")
}

// Defaults fills in sensible defaults for any unset fields and expands ~ in paths.
func (c *Config) Defaults() {
	if c.Server.Listen == "" {
		c.Server.Listen = "127.0.0.1:7878"
	}
	if c.Translate.ChunkSize == 0 {
		c.Translate.ChunkSize = 30
	}
	if c.Translate.Concurrency == 0 {
		c.Translate.Concurrency = 3
	}
	if c.Translate.MaxAttempts == 0 {
		c.Translate.MaxAttempts = 3
	}
	if c.Cache.Path == "" {
		home, _ := os.UserHomeDir()
		c.Cache.Path = filepath.Join(home, ".local", "share", "dualsub", "cache.db")
	} else {
		c.Cache.Path = expandUser(c.Cache.Path)
	}
	if c.Sync.ConnectTimeoutMS == 0 {
		c.Sync.ConnectTimeoutMS = 800
	}
	if c.Sync.RequestTimeoutSeconds == 0 {
		c.Sync.RequestTimeoutSeconds = 360
	}
	if c.Sync.IntervalSeconds == 0 {
		c.Sync.IntervalSeconds = 30
	}
	if c.Sync.TokenFile != "" {
		c.Sync.TokenFile = expandUser(c.Sync.TokenFile)
	}
}

// LoadSyncToken reads the shared-cache secret after environment overrides.
// An explicit token wins; token_file is primarily useful for service installs.
func (c *Config) LoadSyncToken() error {
	if c.Sync.Token != "" || c.Sync.TokenFile == "" {
		return nil
	}
	data, err := os.ReadFile(c.Sync.TokenFile)
	if err != nil {
		return fmt.Errorf("read sync token file: %w", err)
	}
	c.Sync.Token = strings.TrimSpace(string(data))
	if c.Sync.Token == "" {
		return errors.New("sync token file is empty")
	}
	return nil
}

func expandUser(p string) string {
	if !strings.HasPrefix(p, "~/") {
		return p
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return p
	}
	return filepath.Join(home, p[2:])
}

// Save writes the config out as TOML to path. Existing comments are NOT preserved.
// The destination directory is created if missing.
func (c *Config) Save(path string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("ensure config dir: %w", err)
	}
	tmp := path + ".tmp"
	f, err := os.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
	if err != nil {
		return err
	}
	if err := toml.NewEncoder(f).Encode(c); err != nil {
		_ = f.Close()
		_ = os.Remove(tmp)
		return fmt.Errorf("encode toml: %w", err)
	}
	if err := f.Close(); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return os.Rename(tmp, path)
}

// EnabledProviders returns the names of providers that have enough config to be usable.
func (c *Config) EnabledProviders() []string {
	var out []string
	if c.Providers.OpenAI != nil && c.Providers.OpenAI.APIKey != "" {
		out = append(out, "openai")
	}
	if c.Providers.Gemini != nil && c.Providers.Gemini.APIKey != "" {
		out = append(out, "gemini")
	}
	if c.Providers.Ollama != nil {
		out = append(out, "ollama") // no key required
	}
	if c.Providers.Codex != nil {
		out = append(out, "codex") // no key required; auth via `codex login`
	}
	return out
}
