package main

import (
	"path/filepath"
	"testing"

	"github.com/johnny/dualsub-next/daemon/internal/config"
)

func TestRunConfigSyncPreservesExistingConfig(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.toml")
	original := &config.Config{
		Server: config.ServerConfig{Listen: "127.0.0.1:7878"},
		Providers: config.ProvidersConfig{
			Gemini: &config.GeminiProvider{APIKey: "keep-me", DefaultModel: "flash"},
		},
	}
	if err := original.Save(path); err != nil {
		t.Fatal(err)
	}
	if err := runConfigSync([]string{
		"--config", path,
		"--central-url", "http://ubuntu-dev:7879",
		"--token-file", "/secure/sync.token",
	}); err != nil {
		t.Fatal(err)
	}

	got, err := config.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if got.Sync.CentralURL != "http://ubuntu-dev:7879" || got.Sync.Listen != "" {
		t.Fatalf("sync config = %+v", got.Sync)
	}
	if got.Sync.TokenFile != "/secure/sync.token" {
		t.Fatalf("token file = %q", got.Sync.TokenFile)
	}
	if got.Providers.Gemini == nil || got.Providers.Gemini.APIKey != "keep-me" {
		t.Fatalf("provider config was lost: %+v", got.Providers.Gemini)
	}
}

func TestRunConfigSyncRejectsAmbiguousRole(t *testing.T) {
	err := runConfigSync([]string{
		"--config", filepath.Join(t.TempDir(), "config.toml"),
		"--listen", "100.64.0.1:7879",
		"--central-url", "http://central:7879",
		"--token-file", "/secure/sync.token",
	})
	if err == nil {
		t.Fatal("expected role validation error")
	}
}
