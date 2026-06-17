# Design: Codex CLI translation provider

Date: 2026-06-17

## Problem

The daemon's translation relies on HTTP API providers (OpenAI, Gemini, Ollama).
In practice the user runs on Gemini's free tier, which caps
`generate_content_free_tier_requests` at **5 requests/minute**. Subtitle
translation chunks transcripts into many small requests, so real lectures hit
HTTP 429 quickly:

```
PROVIDER_RATE_LIMIT: Quota exceeded for metric:
generativelanguage.googleapis.com/generate_content_free_tier_requests,
limit: 5, model: gemini-3.5-flash
```

The user is logged into the local `codex` CLI (OpenAI Codex, ChatGPT
subscription auth), which draws from a **separate, much larger quota bucket**
than the Gemini API key. We want to let translation route through `codex exec`
as an alternative path that does not consume Gemini quota.

## Goals

1. Add a new translation provider named `codex` that shells out to the local
   `codex exec` CLI instead of calling an HTTP API.
2. It is a **peer provider**, selectable in the popup dropdown alongside
   gemini/openai/ollama. The user picks it manually; there is no automatic
   fallback.
3. Reuse the existing prompt format (`[N] text`) and `ParseResponse` so the
   orchestrator, cache, and SSE layers need no changes.
4. Require no API key — like ollama, it is enabled by the presence of its
   config block; auth is handled by the user's existing `codex login`.

## Non-goals

- **Automatic fallback** when another provider hits 429. Explicitly rejected by
  the user — codex is a manually-selected peer, not a failover. (The
  orchestrator's per-provider retry is unchanged; it does not switch providers.)
- Streaming codex's intermediate/agent output. We only need the final text.
- Parsing codex's `--json` event stream. We capture the final message via the
  `-o` flag instead (simpler).
- Image/multimodal input.
- Extension UI changes. The provider dropdown is populated from
  `/v1/providers`, which returns the daemon's providers map; registering
  `codex` in that map surfaces it automatically. (Verified during
  implementation; if the popup hardcodes a provider list, add codex there as a
  one-line change.)

## Background: the `codex exec` CLI

Confirmed on this machine — `codex-cli 0.133.0`, logged in via ChatGPT.

- `codex exec [PROMPT]` runs non-interactively. If no PROMPT arg (or `-`) is
  given and stdin is piped, the prompt is read from **stdin**.
- `-o, --output-last-message <FILE>` writes **only the agent's final message**
  to FILE — avoids the reasoning/tool-log noise on stdout.
- `-p, --profile <CONFIG_PROFILE>` selects a profile from `~/.codex/config.toml`.
- `-m, --model <MODEL>` selects the model.
- `-s, --sandbox read-only` runs in a read-only sandbox (translation needs no
  file writes or tool actions).
- `--skip-git-repo-check` allows running outside a git repository (the daemon's
  cwd is arbitrary).
- `--color never` keeps output free of ANSI escapes.

## Architecture

The change is fully contained in the daemon. The `codex` provider is just
another implementation of the existing `provider.Provider` interface
(`Name() / DefaultModel() / Translate(ctx, Request)`). To the orchestrator it is
indistinguishable from an HTTP provider — the only difference is that under the
hood `Translate` runs a subprocess instead of an HTTP request.

### Files changed

| File | Change |
|---|---|
| `daemon/internal/provider/codex.go` *(new)* | `codexProvider` implementing `Provider` |
| `daemon/internal/provider/codex_test.go` *(new)* | Unit tests using a fake codex binary |
| `daemon/internal/provider/integration_test.go` | Add `TestCodexLive` (real codex, gated by `CODEX_TEST=1`) |
| `daemon/internal/config/config.go` | Add `CodexProvider` struct + `ProvidersConfig.Codex` + `EnabledProviders()` branch |
| `daemon/internal/config/config_test.go` | Cover codex parse + EnabledProviders |
| `daemon/cmd/dualsub/main.go` | `buildProviders()` codex branch + config template comment |

### `codexProvider`

```go
type CodexOptions struct {
    Bin     string        // default "codex"
    Profile string        // optional → -p
    Model   string        // optional → -m
    Sandbox string        // default "read-only" → -s
    Timeout time.Duration // default ~4m (agents are slower than APIs)
}
```

- `Name()` → `"codex"`
- `DefaultModel()` → `opts.Model` (may be empty; codex uses its own default)
- `Translate(ctx, in)`:
  1. `system, user := BuildPrompt(in)` — reuse existing `[N] text` format.
     codex CLI has no separate system role, so the prompt is
     `system + "\n\n" + user`.
  2. Resolve the binary with `exec.LookPath(opts.Bin)`. Not found →
     `*Error{Code: CodeMissingConfig}`.
  3. Create a temp file for `-o`. Build args:
     `exec --skip-git-repo-check -s <sandbox> --color never -o <tmpfile>`
     plus `-p <profile>` / `-m <model>` when set.
  4. `cmd := exec.CommandContext(ctx, bin, args...)`; feed the prompt via
     `cmd.Stdin`; capture stderr for diagnostics.
  5. On exit, read the `-o` file → that text → `ParseResponse(text, in.Lines,
     "codex")` → `Response{Lines, Raw: text}`.

Passing the prompt on **stdin** (not as an argv) avoids arg-length limits and
shell-escaping of multi-line subtitle text. Capturing the **final message via
`-o`** (not stdout) means we never have to strip codex's reasoning or tool logs.
`ParseResponse` already ignores any non-`[N]` lines, so incidental prose codex
might add does not break parsing as long as each `[N]` line is present.

### Error mapping

| Condition | Code | Retryable |
|---|---|---|
| `exec.LookPath` fails (codex not on PATH) | `MISSING_CONFIG` | no |
| `ctx` canceled / deadline exceeded | `PROVIDER_TIMEOUT` | yes |
| Non-zero exit, stderr matches `rate limit`/`quota`/`usage limit`/`429` | `PROVIDER_RATE_LIMIT` | yes |
| Non-zero exit, other | `PROVIDER_SERVER_ERROR` | yes |
| `-o` file empty or missing `[N]` indices | `PARSE_FAILED` (from `ParseResponse`) | yes |

Retryable errors are handled by the existing orchestrator retry loop
(`max_attempts`), exactly as for HTTP providers.

### Config

```toml
[providers.codex]
# bin = "codex"          # optional; default resolves "codex" on PATH
# profile = ""           # optional; codex exec -p
# model = ""             # optional; codex exec -m
# sandbox = "read-only"  # optional; codex exec -s
```

`EnabledProviders()` returns `codex` whenever the `[providers.codex]` block is
present (no key required, mirroring ollama). If the block is present but the
binary is missing, `Translate` surfaces a clear `MISSING_CONFIG` error at call
time — consistent with how gemini errors on a missing key only when used.

`buildProviders()` in main.go gains:

```go
if c := cfg.Providers.Codex; c != nil {
    out["codex"] = provider.NewCodex(provider.CodexOptions{
        Bin: c.Bin, Profile: c.Profile, Model: c.Model, Sandbox: c.Sandbox,
    })
}
```

## Data flow

```
popup (provider="codex")
  → POST /v1/translate {provider:"codex", lines:[...]}
  → orchestrator chunks lines, per chunk calls codexProvider.Translate
      → BuildPrompt → "[N] text" prompt
      → codex exec --skip-git-repo-check -s read-only --color never
                   -o <tmp> [-p ..] [-m ..]   (prompt via stdin)
      → read <tmp> (final message) → ParseResponse → []TranslatedLine
  → cache + SSE chunk-done back to popup
```

The orchestrator, cache, SSE, and extension paths are untouched.

## Testing

Per the user, real codex may be used for tests (its quota is high).

- **Integration (real codex)** — `TestCodexLive` in `integration_test.go`,
  `//go:build integration`, gated by `CODEX_TEST=1` (skips otherwise). Reuses the
  existing `runLive(t, NewCodex(...))` helper to translate the 3 sample lines and
  assert a complete `[N]` round-trip. This is the primary end-to-end proof and
  will be run during implementation:
  `CODEX_TEST=1 go test -tags=integration ./internal/provider/ -run TestCodexLive -v`.
- **Unit (no codex)** — `codex_test.go` covers the branches a working real codex
  cannot deterministically produce, using a tiny **fake codex binary** (a shell
  script written to a temp dir, wired via `CodexOptions.Bin`):
  - happy path: fake reads stdin, writes a canned `[N]` response to the `-o`
    file → asserts parsed lines.
  - non-zero exit with `rate limit` on stderr → `PROVIDER_RATE_LIMIT`.
  - bin not found → `MISSING_CONFIG`.
- **Config** — `config_test.go`: `[providers.codex]` parses and `EnabledProviders`
  includes `codex`.

## Trade-offs and risks

- **Latency.** Each chunk is a full agent invocation (~10–40s) versus an API
  call (~1–3s). A 55-line lecture (2 chunks at chunk_size 30) is fine; very long
  transcripts will be noticeably slower. This is the accepted cost of dodging the
  Gemini quota — it is the user's stated goal.
- **codex subscription limits.** ChatGPT plans also rate-limit; when hit we map
  to `PROVIDER_RATE_LIMIT`. It is a separate, larger bucket than Gemini's free
  tier, not unlimited.
- **Agent behavior.** `codex exec` is a reasoning agent. Read-only sandbox plus
  a strict `[N]`-only system prompt keep it from taking actions or chatting; any
  stray prose is ignored by `ParseResponse`.
