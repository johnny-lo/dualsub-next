# Codex CLI Translation Provider — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add a `codex` translation provider to the daemon that shells out to the local `codex exec` CLI (ChatGPT subscription auth), selectable in the popup alongside gemini/openai/ollama, to bypass Gemini's free-tier 429.

**Architecture:** A new `provider.Provider` implementation whose `Translate` runs `codex exec` as a subprocess instead of an HTTP call. It reuses the existing `BuildPrompt` (`[N] text`) and `ParseResponse`, so the orchestrator, cache, SSE, and extension are untouched. The prompt is fed on stdin; the agent's final message is captured via `codex exec -o <file>`. Config gains a `[providers.codex]` block (no API key, mirroring ollama).

**Tech Stack:** Go 1.x (`os/exec`), BurntSushi/toml, existing daemon provider framework.

**Spec:** `docs/superpowers/specs/2026-06-17-codex-cli-provider-design.md`

---

## File Structure

| File | Responsibility |
|---|---|
| `daemon/internal/config/config.go` | Add `CodexProvider` struct, `ProvidersConfig.Codex`, `EnabledProviders()` branch |
| `daemon/internal/config/config_test.go` | Cover codex round-trip + EnabledProviders |
| `daemon/internal/provider/codex.go` *(new)* | `codexProvider` — builds args, runs `codex exec`, parses `-o` file, maps errors |
| `daemon/internal/provider/codex_test.go` *(new)* | Unit tests via a fake codex shell script (happy / rate-limit / missing-bin) |
| `daemon/internal/provider/integration_test.go` | Add `TestCodexLive` (real codex, gated by `CODEX_TEST=1`) |
| `daemon/cmd/dualsub/main.go` | `buildProviders()` codex branch + config template comment |

No extension changes: the popup builds its provider dropdown from `client.listProviders()` (`extension/src/popup/App.tsx:574`), which reflects the daemon's providers map.

---

## Task 1: Config support for `[providers.codex]`

**Files:**
- Modify: `daemon/internal/config/config.go`
- Test: `daemon/internal/config/config_test.go`

- [ ] **Step 1: Write the failing test**

Add to `daemon/internal/config/config_test.go`:

```go
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
```

- [ ] **Step 2: Run test to verify it fails**

Run: `cd daemon && go test ./internal/config/ -run TestCodexConfigRoundTripAndEnabled -v`
Expected: compile failure — `undefined: CodexProvider`, `out.Providers.Codex undefined`.

- [ ] **Step 3: Add the struct, the field, and the EnabledProviders branch**

In `daemon/internal/config/config.go`, add the field to `ProvidersConfig` (after `Ollama`):

```go
type ProvidersConfig struct {
	OpenAI *OpenAIProvider `toml:"openai" json:"openai,omitempty"`
	Gemini *GeminiProvider `toml:"gemini" json:"gemini,omitempty"`
	Ollama *OllamaProvider `toml:"ollama" json:"ollama,omitempty"`
	Codex  *CodexProvider  `toml:"codex"  json:"codex,omitempty"`
}
```

Add the struct (after `OllamaProvider`):

```go
type CodexProvider struct {
	Bin     string `toml:"bin"     json:"bin"`
	Profile string `toml:"profile" json:"profile"`
	Model   string `toml:"model"   json:"model"`
	Sandbox string `toml:"sandbox" json:"sandbox"`
}
```

Add the branch to `EnabledProviders()` (after the ollama branch, before `return out`):

```go
	if c.Providers.Codex != nil {
		out = append(out, "codex") // no key required; auth via `codex login`
	}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `cd daemon && go test ./internal/config/ -v`
Expected: PASS (all config tests, including the existing `TestEnabledProviders` which still expects 2 because it sets no codex block).

- [ ] **Step 5: Commit**

```bash
git add daemon/internal/config/config.go daemon/internal/config/config_test.go
git commit -m "feat(config): add [providers.codex] block"
```

---

## Task 2: `codexProvider` implementation

**Files:**
- Create: `daemon/internal/provider/codex.go`
- Test: `daemon/internal/provider/codex_test.go`

- [ ] **Step 1: Write the failing tests (fake codex binary)**

Create `daemon/internal/provider/codex_test.go`:

```go
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
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `cd daemon && go test ./internal/provider/ -run TestCodex -v`
Expected: compile failure — `undefined: NewCodex`, `undefined: CodexOptions`.

- [ ] **Step 3: Write the implementation**

Create `daemon/internal/provider/codex.go`:

```go
package provider

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"regexp"
	"strings"
	"time"
)

// CodexOptions configures the codex CLI provider. No API key is required —
// auth is handled by the user's existing `codex login`.
type CodexOptions struct {
	Bin     string        // default "codex"
	Profile string        // optional → codex exec -p
	Model   string        // optional → codex exec -m
	Sandbox string        // default "read-only" → codex exec -s
	Timeout time.Duration // default 4m (agents are slower than HTTP APIs)
}

type codexProvider struct {
	opts CodexOptions
}

func NewCodex(opts CodexOptions) Provider {
	if opts.Bin == "" {
		opts.Bin = "codex"
	}
	if opts.Sandbox == "" {
		opts.Sandbox = "read-only"
	}
	if opts.Timeout == 0 {
		opts.Timeout = 4 * time.Minute
	}
	return &codexProvider{opts: opts}
}

func (p *codexProvider) Name() string { return "codex" }

func (p *codexProvider) DefaultModel() string { return p.opts.Model }

// codexRateLimitRE detects subscription rate-limit / quota messages on stderr.
var codexRateLimitRE = regexp.MustCompile(`(?i)rate.?limit|quota|usage limit|too many requests|\b429\b`)

func (p *codexProvider) Translate(ctx context.Context, in Request) (Response, error) {
	bin, err := exec.LookPath(p.opts.Bin)
	if err != nil {
		return Response{}, &Error{
			Code: CodeMissingConfig, Provider: "codex",
			Message: "codex CLI not found on PATH: " + p.opts.Bin, Cause: err,
		}
	}

	system, user := BuildPrompt(in)
	prompt := system + "\n\n" + user

	// codex exec -o writes ONLY the agent's final message here, so we never
	// have to strip reasoning / tool logs from stdout.
	outFile, err := os.CreateTemp("", "dualsub-codex-*.txt")
	if err != nil {
		return Response{}, &Error{Code: CodeServerError, Provider: "codex", Message: "create temp file: " + err.Error(), Cause: err}
	}
	outPath := outFile.Name()
	_ = outFile.Close()
	defer os.Remove(outPath)

	ctx, cancel := context.WithTimeout(ctx, p.opts.Timeout)
	defer cancel()

	args := []string{"exec", "--skip-git-repo-check", "-s", p.opts.Sandbox, "--color", "never", "-o", outPath}
	if p.opts.Profile != "" {
		args = append(args, "-p", p.opts.Profile)
	}
	model := in.Model
	if model == "" {
		model = p.opts.Model
	}
	if model != "" {
		args = append(args, "-m", model)
	}

	cmd := exec.CommandContext(ctx, bin, args...)
	cmd.Stdin = strings.NewReader(prompt) // prompt via stdin: no argv length / escaping limits
	var stderr bytes.Buffer
	cmd.Stderr = &stderr

	if runErr := cmd.Run(); runErr != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return Response{}, &Error{Code: CodeTimeout, Provider: "codex", Message: "codex exec did not complete: " + ctxErr.Error(), Retryable: true, Cause: ctxErr}
		}
		msg := truncate(stderr.String(), 500)
		if codexRateLimitRE.MatchString(stderr.String()) {
			return Response{}, &Error{Code: CodeRateLimit, Provider: "codex", Message: msg, Retryable: true, Cause: runErr}
		}
		return Response{}, &Error{Code: CodeServerError, Provider: "codex", Message: "codex exec failed: " + msg, Retryable: true, Cause: runErr}
	}

	data, err := os.ReadFile(outPath)
	if err != nil {
		return Response{}, &Error{Code: CodeParseFailed, Provider: "codex", Message: "read codex output: " + err.Error(), Retryable: true, Cause: err}
	}
	out := string(data)
	lines, err := ParseResponse(out, in.Lines, "codex")
	if err != nil {
		return Response{}, err
	}
	return Response{Lines: lines, Raw: out}, nil
}
```

Note: `truncate` is already defined in `http.go` (same package) — reuse it, do not redefine.

- [ ] **Step 4: Run tests to verify they pass**

Run: `cd daemon && go test ./internal/provider/ -run TestCodex -v`
Expected: PASS — `TestCodexTranslateHappyPath`, `TestCodexTranslateRateLimit`, `TestCodexTranslateMissingBin`, `TestCodexNameAndModel`.

- [ ] **Step 5: Commit**

```bash
git add daemon/internal/provider/codex.go daemon/internal/provider/codex_test.go
git commit -m "feat(provider): codex CLI provider via codex exec"
```

---

## Task 3: Wire codex into the daemon

**Files:**
- Modify: `daemon/cmd/dualsub/main.go`

- [ ] **Step 1: Add the buildProviders branch**

In `daemon/cmd/dualsub/main.go`, inside `buildProviders`, after the ollama branch and before `return out`:

```go
	if c := cfg.Providers.Codex; c != nil {
		out["codex"] = provider.NewCodex(provider.CodexOptions{
			Bin: c.Bin, Profile: c.Profile, Model: c.Model, Sandbox: c.Sandbox,
		})
	}
```

- [ ] **Step 2: Add codex to the config template**

In the `configTemplate` const, after the ollama block, add:

```
# [providers.codex]
# bin = "codex"          # optional; default resolves "codex" on PATH
# profile = ""           # optional; codex exec -p
# model = ""             # optional; codex exec -m
# sandbox = "read-only"  # optional; codex exec -s
```

- [ ] **Step 3: Verify the daemon builds**

Run: `cd daemon && go build ./...`
Expected: no output (success).

- [ ] **Step 4: Run the full daemon test suite**

Run: `cd daemon && go test ./...`
Expected: PASS across all packages.

- [ ] **Step 5: Commit**

```bash
git add daemon/cmd/dualsub/main.go
git commit -m "feat(daemon): register codex provider + config template"
```

---

## Task 4: Real-codex integration test

**Files:**
- Modify: `daemon/internal/provider/integration_test.go`

- [ ] **Step 1: Add the gated live test**

Append to `daemon/internal/provider/integration_test.go` (the file already has `//go:build integration` and the shared `runLive`/`sampleRequest`):

```go
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
```

- [ ] **Step 2: Run it against real codex**

Run: `cd daemon && CODEX_TEST=1 go test -tags=integration ./internal/provider/ -run TestCodexLive -v`
Expected: PASS — log lines show the 3 sample lines translated to zh-TW, e.g. `[1] Hello world. → 你好，世界。`.

If it fails on parsing (`PARSE_FAILED`), inspect the raw output the test logs and, if codex wraps output unexpectedly, confirm `ParseResponse` still finds each `[N]` line. (It tolerates surrounding prose.) Do not loosen the `[N]` contract without cause.

- [ ] **Step 3: Commit**

```bash
git add daemon/internal/provider/integration_test.go
git commit -m "test(provider): live codex integration test (CODEX_TEST=1)"
```

---

## Task 5: Deploy and verify end-to-end

**Files:** none (runtime/config only)

- [ ] **Step 1: Build the daemon binary**

Run: `cd daemon && go build -o dualsub ./cmd/dualsub`
Expected: a `daemon/dualsub` binary is produced.

- [ ] **Step 2: Add the codex provider block to the live config**

Edit `~/.config/dualsub/config.toml` and add (alongside the existing gemini block — both stay, codex is a peer):

```toml
[providers.codex]
sandbox = "read-only"
```

(Leave `bin`/`profile`/`model` unset to use defaults.)

- [ ] **Step 2a: Verify the config still loads**

Run: `cd daemon && ./dualsub config init 2>&1 | head -1 || true` (will refuse — config exists; that's fine, it confirms the path). Then confirm parse by listing enabled providers via the next step.

- [ ] **Step 3: Restart the running daemon**

The daemon (currently pid for `dualsub serve`, ~8 days uptime) must restart to load the new binary and the codex config. Identify and stop it, then start the freshly-built binary:

Run: `pkill -f 'dualsub serve' ; sleep 1 ; cd daemon && nohup ./dualsub serve >/tmp/dualsub.out 2>&1 &`
Expected: `/tmp/dualsub.out` shows `providers: gemini, codex` (order may vary).

Note: if the daemon is normally launched via `dualsub-watch.sh` / a service wrapper, restart it the same way the user does instead of `nohup`, and install the new binary where that wrapper expects it.

- [ ] **Step 4: Verify the daemon advertises codex**

Run: `curl -s http://127.0.0.1:7878/v1/providers`
Expected: JSON array including `{"name":"codex",...}` alongside gemini.

- [ ] **Step 5: Manual browser verification (user)**

Reload the extension is NOT required (no extension code changed), but the popup must re-fetch providers: close and reopen the DualSub popup on a Udemy/Netflix tab. The provider dropdown now lists **codex**. Select it, start a translation, and confirm overlay lines translate without a Gemini 429.

- [ ] **Step 6: Final commit (if any tracked artifacts changed)**

The live `~/.config/dualsub/config.toml` and the built binary are not tracked; nothing to commit here unless `daemon/dualsub` is intentionally checked in (it is not). Confirm `git status` is clean apart from intended files.

---

## Self-Review

- **Spec coverage:**
  - Goal 1 (codex provider shelling to `codex exec`) → Task 2.
  - Goal 2 (peer provider, manual select, no auto-fallback) → Task 3 (registration) + no orchestrator change; dropdown is dynamic.
  - Goal 3 (reuse `BuildPrompt`/`ParseResponse`) → Task 2 implementation.
  - Goal 4 (no API key; enabled by config block) → Task 1 `EnabledProviders` branch.
  - Error mapping table → Task 2 `Translate` + Task 2 tests (rate-limit, missing-bin) + parse-failure via `ParseResponse`.
  - Config block → Task 1 + Task 3 template.
  - Testing (real integration + fake-bin units) → Task 2 + Task 4.
  - Extension non-goal (no change) → confirmed via `App.tsx:574`; Task 5 step 5 covers re-fetch only.
- **Placeholder scan:** none — every code/test step has full content.
- **Type consistency:** `CodexProvider` (config struct, toml/json tags) vs `CodexOptions` (provider options) used consistently; `NewCodex(CodexOptions{...})` signature matches in main.go, codex_test.go, integration_test.go. `Name()`→"codex", `DefaultModel()`→`opts.Model` consistent. Error codes (`CodeMissingConfig`, `CodeRateLimit`, `CodeTimeout`, `CodeServerError`, `CodeParseFailed`) all exist in `provider.go`.
