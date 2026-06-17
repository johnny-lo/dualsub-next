//go:build integration

package provider

import (
	"context"
	"os"
	"testing"
	"time"
)

// Run with:
//   OPENAI_API_KEY=... go test -tags=integration ./internal/provider/ -run TestOpenAILive -v
//   GEMINI_API_KEY=... go test -tags=integration ./internal/provider/ -run TestGeminiLive -v
//   OLLAMA_TEST=1     go test -tags=integration ./internal/provider/ -run TestOllamaLive -v

var sampleRequest = Request{
	SourceLang: "en",
	TargetLang: "zh-TW",
	Lines: []Line{
		{Index: 1, Text: "Hello world."},
		{Index: 2, Text: "How are you today?"},
		{Index: 3, Text: "I love translating subtitles."},
	},
}

func runLive(t *testing.T, p Provider) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	res, err := p.Translate(ctx, sampleRequest)
	if err != nil {
		t.Fatalf("%s translate failed: %v", p.Name(), err)
	}
	if len(res.Lines) != len(sampleRequest.Lines) {
		t.Fatalf("%s got %d lines, want %d (raw=%q)", p.Name(), len(res.Lines), len(sampleRequest.Lines), res.Raw)
	}
	for i, l := range res.Lines {
		if l.Text == "" {
			t.Errorf("%s line %d empty", p.Name(), i+1)
		}
		t.Logf("[%d] %s → %s", l.Index, sampleRequest.Lines[i].Text, l.Text)
	}
}

func TestOpenAILive(t *testing.T) {
	key := os.Getenv("OPENAI_API_KEY")
	if key == "" {
		t.Skip("set OPENAI_API_KEY to run")
	}
	runLive(t, NewOpenAI(OpenAIOptions{APIKey: key}))
}

func TestGeminiLive(t *testing.T) {
	key := os.Getenv("GEMINI_API_KEY")
	if key == "" {
		t.Skip("set GEMINI_API_KEY to run")
	}
	runLive(t, NewGemini(GeminiOptions{APIKey: key}))
}

func TestOllamaLive(t *testing.T) {
	if os.Getenv("OLLAMA_TEST") == "" && os.Getenv("OLLAMA_BASE_URL") == "" {
		t.Skip("set OLLAMA_TEST=1 (and have Ollama running) to run")
	}
	opts := OllamaOptions{}
	if base := os.Getenv("OLLAMA_BASE_URL"); base != "" {
		opts.BaseURL = base
	}
	if model := os.Getenv("OLLAMA_MODEL"); model != "" {
		opts.DefaultModel = model
	}
	runLive(t, NewOllama(opts))
}

func TestCodexLive(t *testing.T) {
	if os.Getenv("CODEX_TEST") == "" {
		t.Skip("set CODEX_TEST=1 (and be logged in via `codex login`) to run")
	}
	opts := CodexOptions{}
	if m := os.Getenv("CODEX_MODEL"); m != "" {
		opts.Model = m
	}
	runLive(t, NewCodex(opts))
}
