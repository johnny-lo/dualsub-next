package provider

import (
	"context"
	"fmt"
	"io"
	"os"
	"testing"
)

// fakeCodexEnv selects the fake behaviour when the test binary re-executes
// itself as `codex exec` (see TestMain).
const fakeCodexEnv = "DUALSUB_FAKE_CODEX"

// TestMain lets the test binary double as a portable fake codex CLI: when
// fakeCodexEnv is set it behaves like `codex exec` (prompt on stdin, output
// path via `-o <path>`) instead of running tests.
func TestMain(m *testing.M) {
	switch os.Getenv(fakeCodexEnv) {
	case "":
		os.Exit(m.Run())
	case "ok":
		out := ""
		args := os.Args[1:]
		for i := 0; i+1 < len(args); i++ {
			if args[i] == "-o" {
				out = args[i+1]
			}
		}
		_, _ = io.Copy(io.Discard, os.Stdin)
		if err := os.WriteFile(out, []byte("[1] 你好\n[2] 世界\n"), 0o644); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(2)
		}
		os.Exit(0)
	case "ratelimit":
		fmt.Fprintln(os.Stderr, "stream error: rate limit reached for gpt-5")
		os.Exit(1)
	}
	os.Exit(3)
}

// fakeCodex returns the path of a fake codex (this test binary) that behaves
// according to mode.
func fakeCodex(t *testing.T, mode string) string {
	t.Helper()
	t.Setenv(fakeCodexEnv, mode)
	return os.Args[0]
}

var codexSample = Request{
	SourceLang: "en",
	TargetLang: "zh-TW",
	Lines: []Line{
		{Index: 1, Text: "Hello"},
		{Index: 2, Text: "World"},
	},
}

func TestCodexTranslateHappyPath(t *testing.T) {
	bin := fakeCodex(t, "ok")
	p := NewCodex(CodexOptions{Bin: bin})
	res, err := p.Translate(context.Background(), codexSample)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(res.Lines) != 2 || res.Lines[0].Text != "你好" || res.Lines[1].Text != "世界" {
		t.Errorf("unexpected lines: %+v", res.Lines)
	}
}

func TestCodexTranslateRateLimit(t *testing.T) {
	bin := fakeCodex(t, "ratelimit")
	p := NewCodex(CodexOptions{Bin: bin})
	_, err := p.Translate(context.Background(), codexSample)
	pe, ok := err.(*Error)
	if !ok {
		t.Fatalf("expected *Error, got %T: %v", err, err)
	}
	if pe.Code != CodeRateLimit {
		t.Errorf("got %s, want PROVIDER_RATE_LIMIT", pe.Code)
	}
	if !pe.Retryable {
		t.Error("rate limit should be retryable")
	}
}

func TestCodexTranslateMissingBin(t *testing.T) {
	p := NewCodex(CodexOptions{Bin: "/nonexistent/codex-xyz-does-not-exist"})
	_, err := p.Translate(context.Background(), codexSample)
	pe, ok := err.(*Error)
	if !ok {
		t.Fatalf("expected *Error, got %T: %v", err, err)
	}
	if pe.Code != CodeMissingConfig {
		t.Errorf("got %s, want MISSING_CONFIG", pe.Code)
	}
}

func TestCodexNameAndModel(t *testing.T) {
	p := NewCodex(CodexOptions{Model: "gpt-5"})
	if p.Name() != "codex" {
		t.Errorf("name: got %q", p.Name())
	}
	if p.DefaultModel() != "gpt-5" {
		t.Errorf("default model: got %q", p.DefaultModel())
	}
}
