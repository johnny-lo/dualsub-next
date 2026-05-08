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
  dualsub version                  Print version
  dualsub help                     Show this message

Config:
  Default path:  ~/.config/dualsub/config.toml
  Env overrides: OPENAI_API_KEY, GEMINI_API_KEY, OLLAMA_BASE_URL, DUALSUB_LISTEN`)
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
	if *addrOverride != "" {
		cfg.Server.Listen = *addrOverride
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

	providers := buildProviders(cfg)
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

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	errCh := make(chan error, 1)
	go func() {
		fmt.Printf("dualsub listening on http://%s\n", cfg.Server.Listen)
		fmt.Printf("  cache:     %s\n", cfg.Cache.Path)
		fmt.Printf("  log:       %s\n", logPath)
		fmt.Printf("  providers: %s\n", strings.Join(enabled, ", "))
		lg.Event("daemon_started", map[string]any{"listen": cfg.Server.Listen, "providers": enabled})
		errCh <- srv.ListenAndServe()
	}()

	select {
	case <-ctx.Done():
		fmt.Println("shutdown signal received")
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		return srv.Shutdown(shutdownCtx)
	case err := <-errCh:
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
`

func runConfig(args []string) error {
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
