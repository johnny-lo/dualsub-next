package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/johnny/dualsub-next/daemon/internal/cache"
	"github.com/johnny/dualsub-next/daemon/internal/config"
	"github.com/johnny/dualsub-next/daemon/internal/logger"
	"github.com/johnny/dualsub-next/daemon/internal/provider"
	"github.com/johnny/dualsub-next/daemon/internal/server"
	"github.com/johnny/dualsub-next/daemon/internal/sharedcache"
	"github.com/johnny/dualsub-next/daemon/internal/translate"
)

func main() {
	if len(os.Args) < 2 {
		usage()
		os.Exit(2)
	}

	switch os.Args[1] {
	case "serve":
		if err := runServe(os.Args[2:]); err != nil {
			fmt.Fprintln(os.Stderr, "error:", err)
			os.Exit(1)
		}
	case "version":
		fmt.Println("dualsub dev")
	case "config":
		if err := runConfig(os.Args[2:]); err != nil {
			fmt.Fprintln(os.Stderr, "error:", err)
			os.Exit(1)
		}
	case "-h", "--help", "help":
		usage()
	default:
		fmt.Fprintf(os.Stderr, "unknown command: %s\n\n", os.Args[1])
		usage()
		os.Exit(2)
	}
}

func usage() {
	fmt.Println(`dualsub — DualSub Next translation daemon

Usage:
  dualsub serve  [--config PATH]   Start the HTTP daemon
  dualsub config init              Write a config template (will not overwrite)
  dualsub config sync [options]    Configure central or client cache sharing
  dualsub version                  Print version
  dualsub help                     Show this message

Config:
  Default path:  ~/.config/dualsub/config.toml
  Env overrides: OPENAI_API_KEY, GEMINI_API_KEY, OLLAMA_BASE_URL,
                 DUALSUB_LISTEN, DUALSUB_SYNC_TOKEN`)
}

func runServe(args []string) error {
	fs := flag.NewFlagSet("serve", flag.ExitOnError)
	cfgPath := fs.String("config", config.DefaultPath(), "path to config TOML")
	addrOverride := fs.String("addr", "", "override server.listen from config")
	if err := fs.Parse(args); err != nil {
		return err
	}

	cfg, err := config.Load(*cfgPath)
	if err != nil {
		return err
	}
	cfg.ApplyEnvOverrides()
	cfg.Defaults()
	if err := cfg.LoadSyncToken(); err != nil {
		return err
	}
	if *addrOverride != "" {
		cfg.Server.Listen = *addrOverride
	}
	if cfg.Sync.Listen != "" && cfg.Sync.CentralURL != "" {
		return errors.New("sync.listen and sync.central_url cannot both be set on one daemon")
	}
	if (cfg.Sync.Listen != "" || cfg.Sync.CentralURL != "") && cfg.Sync.Token == "" {
		return errors.New("sync token, token_file, or DUALSUB_SYNC_TOKEN is required when shared cache is enabled")
	}
	if (cfg.Sync.Listen != "" || cfg.Sync.CentralURL != "") && len(cfg.Sync.Token) < 32 {
		return errors.New("shared-cache sync token must be at least 32 characters")
	}

	enabled := cfg.EnabledProviders()
	if len(enabled) == 0 {
		return fmt.Errorf(`no providers configured.

Set up at least one provider in %s, for example:

    [providers.gemini]
    api_key = "your-key-here"

Or run: dualsub config init`, *cfgPath)
	}

	c, err := cache.Open(cfg.Cache.Path)
	if err != nil {
		return fmt.Errorf("open cache: %w", err)
	}
	defer c.Close()

	logPath := filepath.Join(filepath.Dir(cfg.Cache.Path), "daemon.log")
	lg, err := logger.New(logPath)
	if err != nil {
		return fmt.Errorf("open log: %w", err)
	}
	defer lg.Close()

	baseProviders := buildProviders(cfg)
	providers := baseProviders
	var remoteCache *sharedcache.Client
	if cfg.Sync.CentralURL != "" {
		remoteCache, err = sharedcache.NewClient(sharedcache.ClientOptions{
			BaseURL: cfg.Sync.CentralURL, Token: cfg.Sync.Token,
			ConnectTimeout: time.Duration(cfg.Sync.ConnectTimeoutMS) * time.Millisecond,
			RequestTimeout: time.Duration(cfg.Sync.RequestTimeoutSeconds) * time.Second,
		})
		if err != nil {
			return err
		}
		providers = make(map[string]provider.Provider, len(baseProviders))
		for name, local := range baseProviders {
			providers[name] = sharedcache.NewFallbackProvider(local, remoteCache)
		}
	}
	orch := translate.New(providers, c, translate.Config{
		ChunkSize:   cfg.Translate.ChunkSize,
		Concurrency: cfg.Translate.Concurrency,
		MaxAttempts: cfg.Translate.MaxAttempts,
	})

	srv := server.New(server.Options{
		Addr:         cfg.Server.Listen,
		Orchestrator: orch,
		Providers:    providers,
		Cache:        c,
		Config:       cfg,
		ConfigPath:   *cfgPath,
		Logger:       lg,
	})
	var syncServer *sharedcache.Server
	if cfg.Sync.Listen != "" {
		syncServer = sharedcache.NewServer(sharedcache.ServerOptions{
			Addr: cfg.Sync.Listen, Token: cfg.Sync.Token, Cache: c, Providers: baseProviders,
		})
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	if remoteCache != nil {
		go sharedcache.RunOutbox(ctx, c, remoteCache, time.Duration(cfg.Sync.IntervalSeconds)*time.Second)
	}

	errCh := make(chan error, 2)
	go func() {
		fmt.Printf("dualsub listening on http://%s\n", cfg.Server.Listen)
		fmt.Printf("  cache:     %s\n", cfg.Cache.Path)
		fmt.Printf("  log:       %s\n", logPath)
		fmt.Printf("  providers: %s\n", strings.Join(enabled, ", "))
		lg.Event("daemon_started", map[string]any{"listen": cfg.Server.Listen, "providers": enabled})
		errCh <- srv.ListenAndServe()
	}()
	if syncServer != nil {
		go func() {
			fmt.Printf("  shared:    http://%s\n", cfg.Sync.Listen)
			lg.Event("shared_cache_started", map[string]any{"listen": cfg.Sync.Listen})
			errCh <- syncServer.ListenAndServe()
		}()
	} else if cfg.Sync.CentralURL != "" {
		fmt.Printf("  shared:    %s (local-first client)\n", cfg.Sync.CentralURL)
		lg.Event("shared_cache_client_started", map[string]any{"central_url": cfg.Sync.CentralURL})
	}

	shutdown := func() error {
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		mainErr := srv.Shutdown(shutdownCtx)
		if syncServer != nil {
			if syncErr := syncServer.Shutdown(shutdownCtx); mainErr == nil {
				mainErr = syncErr
			}
		}
		return mainErr
	}

	select {
	case <-ctx.Done():
		fmt.Println("shutdown signal received")
		return shutdown()
	case err := <-errCh:
		_ = shutdown()
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return err
	}
}

func buildProviders(cfg *config.Config) map[string]provider.Provider {
	out := map[string]provider.Provider{}
	if c := cfg.Providers.OpenAI; c != nil && c.APIKey != "" {
		out["openai"] = provider.NewOpenAI(provider.OpenAIOptions{
			APIKey: c.APIKey, BaseURL: c.BaseURL, DefaultModel: c.DefaultModel,
		})
	}
	if c := cfg.Providers.Gemini; c != nil && c.APIKey != "" {
		out["gemini"] = provider.NewGemini(provider.GeminiOptions{
			APIKey: c.APIKey, BaseURL: c.BaseURL, DefaultModel: c.DefaultModel,
		})
	}
	if c := cfg.Providers.Ollama; c != nil {
		out["ollama"] = provider.NewOllama(provider.OllamaOptions{
			BaseURL: c.BaseURL, DefaultModel: c.DefaultModel,
		})
	}
	if c := cfg.Providers.Codex; c != nil {
		out["codex"] = provider.NewCodex(provider.CodexOptions{
			Bin: c.Bin, Profile: c.Profile, Model: c.Model, Sandbox: c.Sandbox,
		})
	}
	return out
}

const configTemplate = `# DualSub Next config — copy and uncomment the providers you want to use.

[server]
listen = "127.0.0.1:7878"

[translate]
chunk_size = 30
concurrency = 3
max_attempts = 3

[cache]
# path = "~/.local/share/dualsub/cache.db"

# Local-first shared cache. Configure exactly one of these per machine.
# On the always-on central node, bind only its Tailscale IP:
# [sync]
# listen = "100.x.y.z:7879"
# token_file = "~/.config/dualsub/sync.token"
#
# On client nodes:
# [sync]
# central_url = "http://ubuntu-dev:7879"
# token_file = "~/.config/dualsub/sync.token"
# connect_timeout_ms = 800
# request_timeout_seconds = 360
# interval_seconds = 30

# Pick at least one provider:

# [providers.gemini]
# api_key = "AIza..."
# default_model = "gemini-2.5-flash"

# [providers.openai]
# api_key = "sk-..."
# default_model = "gpt-4o-mini"

# [providers.ollama]
# base_url = "http://127.0.0.1:11434"
# default_model = "qwen2.5:7b"

# [providers.codex]
# bin = "codex"          # optional; default resolves "codex" on PATH
# profile = ""           # optional; codex exec -p
# model = ""             # optional; codex exec -m
# sandbox = "read-only"  # optional; codex exec -s
`

func runConfig(args []string) error {
	if len(args) > 0 && args[0] == "sync" {
		return runConfigSync(args[1:])
	}
	fs := flag.NewFlagSet("config", flag.ExitOnError)
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 1 || fs.Arg(0) != "init" {
		return errors.New("usage: dualsub config init")
	}
	path := config.DefaultPath()
	if _, err := os.Stat(path); err == nil {
		return fmt.Errorf("config already exists at %s — refusing to overwrite", path)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	if err := os.WriteFile(path, []byte(configTemplate), 0o600); err != nil {
		return err
	}
	fmt.Printf("wrote config template to %s\nedit it to add your provider keys, then run: dualsub serve\n", path)
	return nil
}

func runConfigSync(args []string) error {
	fs := flag.NewFlagSet("config sync", flag.ContinueOnError)
	cfgPath := fs.String("config", config.DefaultPath(), "path to config TOML")
	listen := fs.String("listen", "", "Tailscale IP and port for the central node")
	centralURL := fs.String("central-url", "", "shared-cache URL for a client node")
	tokenFile := fs.String("token-file", "", "path to the shared sync token file")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if (*listen == "") == (*centralURL == "") {
		return errors.New("set exactly one of --listen or --central-url")
	}
	if *tokenFile == "" {
		return errors.New("--token-file is required")
	}
	cfg, err := config.Load(*cfgPath)
	if err != nil {
		return err
	}
	cfg.Sync.Listen = *listen
	cfg.Sync.CentralURL = *centralURL
	cfg.Sync.Token = ""
	cfg.Sync.TokenFile = *tokenFile
	if err := cfg.Save(*cfgPath); err != nil {
		return err
	}
	mode := "client"
	destination := *centralURL
	if *listen != "" {
		mode = "central"
		destination = *listen
	}
	fmt.Printf("saved shared-cache %s config for %s to %s\n", mode, destination, *cfgPath)
	return nil
}
