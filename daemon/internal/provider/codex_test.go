package provider

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

// writeFakeCodex writes an executable shell script to a temp dir and returns
// its path. The script body must behave like `codex exec`: it receives the
// prompt on stdin and the output path via `-o <path>`.
func writeFakeCodex(t *testing.T, body string) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "codex")
	if err := os.WriteFile(path, []byte("#!/bin/sh\n"+body), 0o755); err != nil {
		t.Fatalf("write fake codex: %v", err)
	}
	return path
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
	// Drain stdin, find -o <path>, write a canned [N] response there.
	bin := writeFakeCodex(t, `
out=""
while [ $# -gt 0 ]; do
  case "$1" in
    -o) out="$2"; shift 2 ;;
    *) shift ;;
  esac
done
cat > /dev/null
printf '[1] 你好\n[2] 世界\n' > "$out"
`)
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
	bin := writeFakeCodex(t, `
echo "stream error: rate limit reached for gpt-5" >&2
exit 1
`)
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
