# Live Model Picker + agy Provider Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Let the popup pick a model (and, for codex, an effort) from lists fetched live from each provider, and replace the API-key Gemini provider with the `agy` CLI.

**Architecture:** Providers that can list models implement an optional `provider.ModelLister`; the daemon serves `GET /v1/providers/{name}/models` with a 5-minute in-memory cache. `Effort` joins `Model` on every translate path (orchestrator, shared-cache resolve). The extension keeps one remembered selection in `chrome.storage.local`; the popup edits it, and the content script reads it for auto-translate and live mode.

**Tech Stack:** Go 1.25 (`net/http` ServeMux patterns, `os/exec`), TypeScript + React 18 + Antd 5, Vitest (jsdom).

**Spec:** `docs/superpowers/specs/2026-10-26-live-model-picker-agy-design.md`

## Global Constraints

- No hardcoded model lists anywhere. The existing single fallbacks `gpt-4o-mini` (OpenAI) and `qwen2.5:7b` (Ollama) stay; add no new ones.
- Tests never run the real `codex` or `agy`; use the self-re-exec fake CLI pattern (`TestMain` + env var), which works on Windows.
- agy is never launched with `--dangerously-skip-permissions`; each agy call runs in a fresh empty temp dir that is removed afterwards.
- Model-list cache TTL: exactly 5 minutes; `?refresh=1` bypasses and replaces it; failures are never cached.
- agy prompt limit before launch: 24,000 characters → `CodeContextTooLong`.
- Code comments and commit messages in English; commits end with `Co-Authored-By: Claude Opus 5.5 (1M context) <noreply@anthropic.com>`.
- Storage key for the remembered selection: `dualsubSelection`.
- Daemon test command: `cd daemon && go test -race ./...`. Extension: `cd extension && npm test && npm run typecheck`.

## Review Focus

1. Subtitle text containing `&`, `|`, `%`, `"`, `'` and line breaks reaches agy as the exact prompt, byte for byte (Task 2 test `TestAgyPromptPassedVerbatim`).
2. A model or effort value with spaces or shell metacharacters arriving over HTTP is rejected with `CodeBadRequest` before any CLI starts (Task 1 `TestCodexRejectsUnsafeEffortAndModel`, Task 2 `TestAgyRejectsUnsafeModel`).
3. agy prints "Please sign in…" or a non-SUCCESS JSON status while exiting 0: this must be a typed error, not a parse failure or an empty translation (Task 2 `TestAgyTranslateSignedOut`, `TestAgyTranslateNonSuccessStatus`).
4. A failed model listing is not cached: the next request retries, and a later success is served (Task 6 `TestProviderModelsErrorNotCached`).
5. The remembered model is missing from a freshly fetched list → "CLI default"; a failed fetch never clears it (Task 7 `reconcileChoice` tests; the popup only calls `reconcileChoice` with a successful list, Task 9).

---

### Task 1: Model types, codex model listing and effort

**Files:**
- Modify: `daemon/internal/provider/provider.go`
- Modify: `daemon/internal/provider/codex.go`
- Create: `daemon/internal/provider/fake_cli_test.go` (move `TestMain` and `fakeCodex` here from `codex_test.go`)
- Create: `daemon/internal/provider/testdata/codex_models.json`
- Modify: `daemon/internal/provider/codex_test.go`

**Interfaces:**
- Produces (in `provider.go`):
  ```go
  // Request gains:
  Effort string // empty → provider/CLI default; only codex uses it
  type ModelInfo struct {
      ID            string   `json:"id"`
      Label         string   `json:"label"`
      Efforts       []string `json:"efforts,omitempty"`
      DefaultEffort string   `json:"default_effort,omitempty"`
  }
  type ModelLister interface {
      ListModels(ctx context.Context) ([]ModelInfo, error)
  }
  var ErrListUnsupported = errors.New("provider cannot list models")
  func validCLIValue(s string) bool // matches ^[A-Za-z0-9._:/-]+$
  ```
- Produces (in `codex.go`): `func (p *codexProvider) ListModels(ctx context.Context) ([]ModelInfo, error)` and `func parseCodexModels(raw []byte) ([]ModelInfo, error)`.
- Produces (in `fake_cli_test.go`): `fakeCLI(t *testing.T, mode string) string`, which sets `DUALSUB_FAKE_CLI=<mode>` and returns `os.Args[0]`. In mode `args`, the fake writes its argv (JSON array, after argv[0]), its working directory and the number of entries in that directory at start to `$DUALSUB_FAKE_ARGS_FILE`, as `{"args":[...],"cwd":"...","cwd_entries":0,"stdin":"..."}`. It then behaves like `ok`. Keep `fakeCodex` as a thin alias so the existing tests still compile. Task 2 adds agy modes to the same switch.

- [ ] **Step 1: Move the fake CLI into `fake_cli_test.go` and add the `args` recording mode and a `codex-models` mode**

`codex-models` prints `testdata/codex_models.json` to stdout when argv is `debug models`. The fixture is a trimmed copy of the real catalog with these entries (fields `slug`, `display_name`, `visibility`, `priority`, `default_reasoning_level`, `supported_reasoning_levels: [{"effort": ...}]`):

| slug | display_name | visibility | priority | default | efforts |
|---|---|---|---|---|---|
| gpt-5.6-sol | GPT-5.6-Sol | list | 6 | low | low, medium, high, xhigh, max, ultra |
| gpt-6-astra | GPT-6-Astra | list | 2 | low | low, medium, high, xhigh, max, ultra |
| gpt-reserve | GPT-Reserve | hide | 4 | medium | low, medium |

- [ ] **Step 2: Write the failing tests in `codex_test.go`**

```go
func TestParseCodexModelsFiltersHiddenAndSortsByPriority(t *testing.T) {
    // fixture → []ModelInfo{
    //   {ID:"gpt-6-astra", Label:"GPT-6-Astra", Efforts:[low..ultra], DefaultEffort:"low"},
    //   {ID:"gpt-5.6-sol", Label:"GPT-5.6-Sol", Efforts:[low..ultra], DefaultEffort:"low"},
    // }  (gpt-reserve dropped)
}
func TestParseCodexModelsRejectsGarbage(t *testing.T)        // "not json" → *Error CodeParseFailed
func TestCodexListModelsRunsDebugModels(t *testing.T)        // fakeCLI "codex-models"; 2 models
func TestCodexTranslatePassesEffort(t *testing.T)            // mode "args"; Request{Model:"gpt-5.6-sol", Effort:"low"} → args contain "-m","gpt-5.6-sol" and "-c","model_reasoning_effort=low"
func TestCodexTranslateOmitsEffortWhenEmpty(t *testing.T)    // no "-c" arg at all
func TestCodexRejectsUnsafeEffortAndModel(t *testing.T)      // Effort:"low & calc", Model:"a b" → CodeBadRequest, not retryable, args file never written
```

- [ ] **Step 3: Run them to verify they fail**

Run: `cd daemon && go test ./internal/provider/ -run 'Codex' -v`
Expected: compile failure (`ModelInfo`, `parseCodexModels`, `Effort` undefined).

- [ ] **Step 4: Implement**

- `parseCodexModels`: keep `visibility == "list"`, sort ascending by `priority`, efforts from `supported_reasoning_levels[].effort` in catalog order.
- `ListModels`: `exec.CommandContext(ctx, bin, "debug", "models")` with a 30 s timeout. A missing bin gives `CodeMissingConfig`, as in `Translate`. A non-zero exit gives `CodeServerError` with truncated stderr.
- `Translate`: validate a non-empty model and effort with `validCLIValue` before building args. When the effort is non-empty, append `-c`, `model_reasoning_effort=<effort>`. Leave it unquoted: codex treats an unparseable TOML value as a literal string, and that avoids quoting through an npm `codex.cmd` on Windows.

- [ ] **Step 5: Run the provider tests**

Run: `cd daemon && go test -race ./internal/provider/`
Expected: PASS

- [ ] **Step 6: Commit** — `feat(provider): codex lists models live and accepts reasoning effort`

---

### Task 2: agy provider

**Files:**
- Create: `daemon/internal/provider/agy.go`
- Create: `daemon/internal/provider/agy_test.go`
- Modify: `daemon/internal/provider/fake_cli_test.go` (agy modes)

**Interfaces:**
- Consumes: `ModelInfo`, `validCLIValue`, `BuildPrompt`, `ParseResponse`, and `fakeCLI` from Task 1.
- Produces:
  ```go
  type AgyOptions struct {
      Bin     string        // default "agy"
      Model   string        // optional default → --model
      Timeout time.Duration // default 4m
  }
  func NewAgy(opts AgyOptions) Provider   // Name() "agy", DefaultModel() opts.Model
  func (p *agyProvider) ListModels(ctx context.Context) ([]ModelInfo, error)
  func parseAgyModels(stdout string) []ModelInfo
  const agyMaxPromptChars = 24000
  ```

- [ ] **Step 1: Add the agy fake modes**

All modes record args, cwd and stdin like `args` mode when `DUALSUB_FAKE_ARGS_FILE` is set.

| mode | behaviour |
|---|---|
| `agy-ok` | stdout `{"conversation_id":"c1","status":"SUCCESS","response":"[1] 你好\n[2] 世界\n","usage":{}}`, exit 0 |
| `agy-signin` | stdout `Please sign in to use Antigravity`, exit 0 |
| `agy-badmodel` | stderr `model "nope" is not recognized`, exit 1 |
| `agy-ratelimit` | stderr `RESOURCE_EXHAUSTED: quota exceeded (429)`, exit 1 |
| `agy-status-error` | stdout `{"status":"ERROR","response":"internal"}`, exit 0 |
| `agy-models` | stderr `Fetching available models...`; stdout `gemini-3.8-flash-low\tGemini 3.8 Flash (Low)\nclaude-sonnet-4-6\tClaude Sonnet 4.6 (Thinking)\n` |
| `agy-models-signin` | stdout `Please sign in to use Antigravity`, exit 0 |

- [ ] **Step 2: Write the failing tests in `agy_test.go`**

```go
func TestAgyTranslateHappyPath(t *testing.T)          // agy-ok → lines 你好/世界
func TestAgyTranslateArgs(t *testing.T)               // Model "gemini-3.8-flash-low": args == ["-p", <prompt>, "--output-format", "json", "--print-timeout", "300s", "--model", "gemini-3.8-flash-low"] (Timeout 4m → 240+60)
                                                      // no "--dangerously-skip-permissions"; no "--model" when Model == "" and opts.Model == ""
func TestAgyRunsInFreshEmptyDirRemovedAfter(t *testing.T) // recorded cwd != os.Getwd(), cwd_entries == 0, and cwd no longer exists after Translate returns
func TestAgyPromptPassedVerbatim(t *testing.T)        // a line `Tom & "Jerry" | 100% 'ok'\nnext` → recorded -p value == system+"\n\n"+user from BuildPrompt
func TestAgyPromptTooLong(t *testing.T)               // lines totalling > 24000 chars → CodeContextTooLong, not retryable, fake never launched
func TestAgyTranslateSignedOut(t *testing.T)          // agy-signin → CodeMissingConfig, not retryable
func TestAgyTranslateUnknownModel(t *testing.T)       // agy-badmodel → CodeBadRequest, not retryable
func TestAgyTranslateRateLimit(t *testing.T)          // agy-ratelimit → CodeRateLimit, retryable
func TestAgyTranslateNonSuccessStatus(t *testing.T)   // agy-status-error → CodeServerError, retryable
func TestAgyRejectsUnsafeModel(t *testing.T)          // Model "x; rm" → CodeBadRequest, fake never launched
func TestParseAgyModels(t *testing.T)                 // → [{gemini-3.8-flash-low, "Gemini 3.8 Flash (Low)"}, {claude-sonnet-4-6, "Claude Sonnet 4.6 (Thinking)"}]; blank lines and lines without a tab skipped; no Efforts
func TestAgyListModelsSignedOut(t *testing.T)         // agy-models-signin → *Error CodeMissingConfig, message "agy is not signed in"
func TestAgyMissingBin(t *testing.T)                  // CodeMissingConfig
```

- [ ] **Step 3: Run them to verify they fail**

Run: `cd daemon && go test ./internal/provider/ -run 'Agy' -v`
Expected: compile failure (`NewAgy` undefined).

- [ ] **Step 4: Implement `agy.go`, modelled on `codex.go`**

- Prompt: `system + "\n\n" + user`, the same as codex, passed as the argv value after `-p`.
- Run order: check the prompt length, validate the model, resolve the bin, make the temp dir, run with `cmd.Dir` set to it, then `os.RemoveAll` it.
- On Windows (`runtime.GOOS == "windows"`), if the resolved bin's extension is `.cmd` or `.bat`, fail with `CodeMissingConfig`: "agy resolves to a .cmd/.bat shim; subtitle text cannot be passed safely on its command line".
- Classify a failure in this order over stdout+stderr:
  1. `Please sign in` → missing config
  2. `is not recognized` → bad request
  3. the codex rate-limit regexp → rate limit
  4. context deadline → timeout
  5. any other non-zero exit → server error
- On exit 0, unmarshal `{status, response}`. A non-JSON stdout falls through to the same classifier and then `CodeParseFailed` (retryable). A status other than `SUCCESS` gives `CodeServerError` with the truncated response. Otherwise return `ParseResponse(response, in.Lines, "agy")`.
- `ListModels`: run `bin models` with a 30 s timeout. A `Please sign in` on either stream gives `CodeMissingConfig` ("agy is not signed in"). Otherwise return `parseAgyModels(stdout)`.

- [ ] **Step 5: Run the provider tests**

Run: `cd daemon && go test -race ./internal/provider/`
Expected: PASS

- [ ] **Step 6: Commit** — `feat(provider): add agy CLI provider with live model listing`

---

### Task 3: OpenAI and Ollama model listing

**Files:**
- Modify: `daemon/internal/provider/http.go`
- Modify: `daemon/internal/provider/openai.go`
- Modify: `daemon/internal/provider/ollama.go`
- Create: `daemon/internal/provider/list_models_test.go`

**Interfaces:**
- Produces:
  ```go
  func (h *httpClient) getJSON(ctx context.Context, urlStr string, headers map[string]string) ([]byte, error) // same error mapping as postJSON
  func (p *openaiProvider) ListModels(ctx context.Context) ([]ModelInfo, error) // GET {BaseURL}/models, Authorization: Bearer <key>
  func (p *ollamaProvider) ListModels(ctx context.Context) ([]ModelInfo, error) // GET {BaseURL}/api/tags
  func parseOpenAIModels(raw []byte) ([]ModelInfo, error)
  func parseOllamaModels(raw []byte) ([]ModelInfo, error)
  ```

- [ ] **Step 1: Write the failing tests (`httptest.Server`)**

```go
func TestOpenAIListModelsFiltersNonTextAndSorts(t *testing.T)
// data ids: gpt-4o-mini, text-embedding-3-small, tts-1, whisper-1, dall-e-3, gpt-image-1,
// gpt-4o-audio-preview, gpt-4o-realtime-preview, omni-moderation-latest, gpt-4o-transcribe, gpt-4.1
// → IDs exactly [gpt-4.1, gpt-4o-mini] (sorted), Label == ID; request carried "Authorization: Bearer k"
func TestOpenAIListModelsUnauthorized(t *testing.T)   // 401 → CodeInvalidKey
func TestOllamaListModels(t *testing.T)               // {"models":[{"name":"qwen2.5:7b"},{"name":"llama3.2:3b"}]} → same order, Label == ID
func TestOllamaListModelsUnreachable(t *testing.T)    // closed server → CodeNetwork
```

- [ ] **Step 2: Run them to verify they fail**

Run: `cd daemon && go test ./internal/provider/ -run 'ListModels' -v`
Expected: compile failure.

- [ ] **Step 3: Implement**

`getJSON` shares the response handling with `postJSON`; extract a helper rather than duplicate. The OpenAI non-text filter drops ids containing any of: `embedding`, `tts`, `whisper`, `dall-e`, `image`, `audio`, `realtime`, `moderation`, `transcribe`.

- [ ] **Step 4: Run the provider tests**

Run: `cd daemon && go test -race ./internal/provider/`
Expected: PASS

- [ ] **Step 5: Commit** — `feat(provider): list OpenAI and Ollama models from their APIs`

---

### Task 4: Remove the Gemini API provider; wire agy into config

**Files:**
- Delete: `daemon/internal/provider/gemini.go`
- Modify: `daemon/internal/provider/integration_test.go` (drop `TestGeminiLive` and its header line)
- Modify: `daemon/internal/provider/provider.go` (package doc comment: providers are OpenAI, Ollama, codex CLI, agy CLI, Claude stub)
- Modify: `daemon/internal/provider/http.go` (comment on `apiKeyParamRE` only: generic URL credentials; keep the code)
- Modify: `daemon/internal/config/config.go`, `daemon/internal/config/config_test.go`
- Modify: `daemon/cmd/dualsub/main.go`, `daemon/cmd/dualsub/main_test.go`
- Modify: `daemon/internal/server/server_test.go` (only the `TestConfigGetPut` gemini payload)
- Modify: `README.md`, `codebase.md`

**Interfaces:**
- Consumes: `NewAgy(AgyOptions)` from Task 2.
- Produces: `config.AgyProvider{Bin string \`toml:"bin" json:"bin"\`; Model string \`toml:"model" json:"model"\`}` and the field `Providers.Agy *AgyProvider \`toml:"agy" json:"agy,omitempty"\``. `EnabledProviders()` includes `"agy"` when the section exists, in order openai, ollama, codex, agy.

- [ ] **Step 1: Update the tests first**

- `config_test.go`:
  - Replace the Gemini round-trip with an agy round-trip: `Bin "agy"`, `Model "gemini-3.8-flash-low"`.
  - `EnabledProviders` with OpenAI (no key), Ollama, Codex and Agy set gives `[ollama codex agy]`.
  - Delete the `GEMINI_API_KEY` env test.
  - Add `TestLoadIgnoresLegacyGeminiSection`: a TOML with `[providers.gemini]\napi_key = "x"` plus `[providers.agy]` loads without error, and `EnabledProviders() == [agy]`.
- `main_test.go`: preserve an agy config instead of Gemini.
- `server_test.go` `TestConfigGetPut`: put and verify `"agy":{"bin":"agy","model":""}` instead of Gemini.

- [ ] **Step 2: Run them to verify they fail**

Run: `cd daemon && go test ./internal/config/ ./cmd/... ./internal/server/ -run 'Config|Enabled|Legacy|Preserve' -v`
Expected: compile failure (`AgyProvider` undefined).

- [ ] **Step 3: Implement**

- Delete the Gemini code paths: struct, field, env override, `EnabledProviders` branch, `buildProviders` branch, template block, and the `GEMINI_API_KEY` mention in `dualsub help`.
- Add the agy branch to `buildProviders` and the template:

  ```toml
  # [providers.agy]
  # bin = "agy"            # optional; default resolves "agy" on PATH
  # model = ""             # optional default, e.g. "gemini-3.8-flash-low"
  ```
- In `runServe`, the "no providers configured" example becomes `[providers.agy]` plus `[providers.codex]`.
- README: the provider list becomes OpenAI, Ollama, codex CLI and agy CLI. The quick-start example uses `[providers.agy]`. Drop the `TestGeminiLive` line.
- codebase.md: point "new provider template" at `openai.go` (HTTP) and `codex.go`/`agy.go` (CLI), and drop the Gemini key-in-URL gotcha.

- [ ] **Step 4: Run the whole daemon suite and vet**

Run: `cd daemon && go vet ./... && go test -race ./... && ! git grep -n -E 'providers\.gemini|GEMINI_API_KEY|NewGemini|GeminiProvider|GeminiOptions' -- . ../README.md`
Expected: PASS, and the grep prints nothing. `gemini` may remain in agy model names such as `gemini-3.8-flash-low`, and as an arbitrary provider-name string in tests.

- [ ] **Step 5: Commit** — `feat!: replace the Gemini API provider with the agy CLI provider`

---

### Task 5: Effort through the orchestrator and the shared cache

**Files:**
- Modify: `daemon/internal/translate/orchestrator.go`, `daemon/internal/translate/orchestrator_test.go`
- Modify: `daemon/internal/sharedcache/protocol.go`, `client.go`, `server.go`, `sharedcache_test.go`

**Interfaces:**
- Consumes: `provider.Request.Effort`, `provider.ModelLister`, `provider.ErrListUnsupported` (Task 1).
- Produces:
  - `translate.Input.Effort string`, passed into every `provider.Request` that `runChunk` builds.
  - `resolveRequest.Effort string \`json:"effort,omitempty"\``.
  - `func (p *FallbackProvider) ListModels(ctx context.Context) ([]provider.ModelInfo, error)`: delegates to the local provider when it implements `ModelLister`, else returns `provider.ErrListUnsupported`. It never asks the central node.

- [ ] **Step 1: Write the failing tests**

```go
// orchestrator_test.go
func TestTranslatePassesEffortToProvider(t *testing.T) // Input{Effort:"low"} → every captured provider.Request has Effort "low"
// sharedcache_test.go
func TestResolveForwardsEffort(t *testing.T)            // client Resolve with Request{Effort:"low"} → central's provider sees Effort "low"
func TestFallbackProviderListModelsDelegatesLocally(t *testing.T) // local lister returns 1 model → same slice; central never contacted
func TestFallbackProviderListModelsUnsupported(t *testing.T)      // local without ListModels → errors.Is(err, provider.ErrListUnsupported)
```

- [ ] **Step 2: Run them to verify they fail**

Run: `cd daemon && go test ./internal/translate/ ./internal/sharedcache/ -run 'Effort|ListModels' -v`
Expected: compile failure / FAIL.

- [ ] **Step 3: Implement**

Plumb `Effort` through: `Input` → `runChunk`'s request; `Client.Resolve` → `resolveRequest`; the resolver's `prov.Translate` request. Jobs keep storing the model only (spec: effort in `/v1/jobs` is optional, and it is skipped).

- [ ] **Step 4: Run the daemon suite**

Run: `cd daemon && go test -race ./...`
Expected: PASS

- [ ] **Step 5: Commit** — `feat: carry reasoning effort through translation and shared-cache resolve`

---

### Task 6: Models endpoint and `effort` on `/v1/translate`

**Files:**
- Create: `daemon/internal/server/models.go`
- Modify: `daemon/internal/server/server.go` (route + `models *modelCache` field)
- Modify: `daemon/internal/server/handlers.go` (`translateRequest.Effort`)
- Create: `daemon/internal/server/models_test.go`

**Interfaces:**
- Consumes: `provider.ModelLister`, `provider.ErrListUnsupported`, `translate.Input.Effort`.
- Produces:
  - Route `mux.HandleFunc("GET /v1/providers/{name}/models", s.handleProviderModels)`.
  - Response `200 {"models":[ModelInfo...],"fetched_at":"<RFC3339 UTC>"}` (`models` is `[]`, never `null`).
  - Errors: `404 {"error":"unknown provider: <name>"}`, `501 {"error":"provider cannot list models"}`, `502 {"error":"<err.Error()>"}`.
  - `type modelCache struct { mu sync.Mutex; ttl time.Duration; now func() time.Time; entries map[string]modelCacheEntry }`, built in `New` with `ttl: 5 * time.Minute, now: time.Now`.
  - `translateRequest.Effort string \`json:"effort,omitempty"\``, copied into `translate.Input.Effort`.

- [ ] **Step 1: Write the failing tests**

```go
func TestProviderModelsListsAndCaches(t *testing.T)       // two GETs within 5 min → 1 ListModels call; body has models + fetched_at
func TestProviderModelsCacheExpires(t *testing.T)         // advance now by 5m+1s → 2nd call hits the lister
func TestProviderModelsRefreshBypassesCache(t *testing.T) // ?refresh=1 → lister called again, and the new result replaces the cache
func TestProviderModelsErrorNotCached(t *testing.T)       // 1st: lister error → 502 {"error":...}; 2nd: lister ok → 200
func TestProviderModelsUnknownProvider(t *testing.T)      // 404
func TestProviderModelsUnsupported(t *testing.T)          // non-lister provider → 501
func TestProviderModelsEmptyListIsArray(t *testing.T)     // lister returns nil → body `"models":[]`
func TestTranslateForwardsEffort(t *testing.T)            // POST /v1/translate {"effort":"low",...} → mock provider saw Effort "low"
```

Give the test server's mock provider an optional lister (calls counter, scripted results). Add a second, non-lister provider for the 501 case; extending `newTestServerWith` to take extra providers is fine.

- [ ] **Step 2: Run them to verify they fail**

Run: `cd daemon && go test ./internal/server/ -run 'ProviderModels|ForwardsEffort' -v`
Expected: FAIL (404 from the mux / compile errors).

- [ ] **Step 3: Implement `models.go` and wire it**

Hold the mutex only around cache reads and writes, not across `ListModels`. Two concurrent cold requests may both call the CLI; that is acceptable.

- [ ] **Step 4: Run the daemon suite with the race detector**

Run: `cd daemon && go vet ./... && go test -race ./...`
Expected: PASS

- [ ] **Step 5: Commit** — `feat(server): serve live model lists per provider`

---

### Task 7: Extension shared layer — client, selection, messages, options page

**Files:**
- Modify: `extension/src/shared/DaemonClient.ts`, `extension/src/shared/DaemonClient.test.ts`
- Create: `extension/src/shared/selection.ts`, `extension/src/shared/selection.test.ts`
- Modify: `extension/src/shared/messaging.ts`
- Modify: `extension/src/options/App.tsx` (remove the Gemini card; fix the merge comment)

**Interfaces:**
- Produces (`DaemonClient.ts`):
  ```ts
  export interface ModelInfo { id: string; label: string; efforts?: string[]; default_effort?: string }
  export interface ModelList { models: ModelInfo[]; fetched_at: string }
  listModels(provider: string, opts?: { refresh?: boolean }): Promise<ModelList>
  // GET `${baseURL}/v1/providers/${encodeURIComponent(provider)}/models` + (refresh ? '?refresh=1' : '')
  // non-2xx → throw new Error(body.error ?? `HTTP ${status}`)
  // TranslateRequest gains: effort?: string
  // DaemonConfig.providers: gemini removed
  ```
- Produces (`selection.ts`):
  ```ts
  export const SELECTION_KEY = 'dualsubSelection'
  export interface ModelChoice { model: string; effort: string }   // '' = CLI/config default
  export interface Selection { provider: string; perProvider: Record<string, ModelChoice> }
  export const CLI_DEFAULT: ModelChoice                             // { model: '', effort: '' }
  export function parseSelection(raw: unknown): Selection | null
  export function choiceFor(sel: Selection | null, provider: string): ModelChoice
  export function withProvider(sel: Selection | null, provider: string): Selection
  export function withChoice(sel: Selection | null, provider: string, choice: ModelChoice): Selection
  export function reconcileProvider(sel: Selection | null, offered: string[]): Selection | null
  export function defaultChoiceFor(model: ModelInfo): ModelChoice
  export function reconcileChoice(choice: ModelChoice, models: ModelInfo[]): ModelChoice // call only with a successfully fetched list
  export function requestTarget(sel: Selection | null, fallbackProvider: string): { provider: string; model?: string; effort?: string }
  export function loadSelection(): Promise<Selection | null>
  export function saveSelection(sel: Selection): Promise<void>
  ```
- Produces (`messaging.ts`): the active `TranslateStatus` variant gains `model?: string; effort?: string`. `SET_OVERLAY.autoTranslate.provider` and `SET_LIVE_MODE.provider` stay, used only as the legacy fallback.

- [ ] **Step 1: Write the failing tests**

`selection.test.ts`:

```ts
it('parseSelection rejects non-objects and missing provider; drops malformed perProvider entries')
it('choiceFor returns CLI_DEFAULT for an unknown provider')
it('withChoice keeps other providers\' choices')                    // codex sol/low survives setting agy
it('reconcileProvider falls back to the first offered provider')    // {provider:'gemini'} + ['agy','codex'] → provider 'agy', perProvider kept
it('reconcileProvider returns null when nothing is offered')
it('reconcileChoice: model missing from list → CLI_DEFAULT')
it('reconcileChoice: effort not supported → model default_effort')  // sol + 'turbo' → sol + 'low'
it('reconcileChoice: model without efforts → effort cleared')       // agy model + 'low' → effort ''
it('reconcileChoice: CLI_DEFAULT stays CLI_DEFAULT')
it('defaultChoiceFor uses default_effort, or \'\' when absent')
it('requestTarget omits model/effort for CLI_DEFAULT and falls back to the legacy provider when sel is null')
// {provider:'codex', perProvider:{codex:{model:'gpt-5.6-sol',effort:'low'}}} → {provider:'codex', model:'gpt-5.6-sol', effort:'low'}
// null + 'gemini' → {provider:'gemini'}
```

`DaemonClient.test.ts`:

```ts
it('listModels GETs the encoded provider path and returns the body')
it('listModels adds ?refresh=1 when asked')
it('listModels throws the daemon error text on non-2xx')            // 502 {"error":"agy is not signed in"} → message 'agy is not signed in'
```

- [ ] **Step 2: Run them to verify they fail**

Run: `cd extension && npx vitest run src/shared`
Expected: FAIL (module/functions missing).

- [ ] **Step 3: Implement**

`selection.ts` is pure functions plus thin `chrome.storage.local` wrappers. `loadSelection` resolves to `null` if storage throws. Remove the Gemini card from the options page.

- [ ] **Step 4: Run tests and typecheck**

Run: `cd extension && npm test && npm run typecheck`
Expected: PASS. All the type changes are additive, so the existing callers still compile.

- [ ] **Step 5: Commit** — `feat(extension): model list client and remembered model selection`

---

### Task 8: Content script follows the current selection

**Files:**
- Modify: `extension/src/content/index.ts`

**Interfaces:**
- Consumes: `SELECTION_KEY`, `parseSelection`, `requestTarget` and `Selection` (Task 7).

- [ ] **Step 1: Implement**

- Keep `let selection: Selection | null = null`. `bootstrap()` loads `SELECTION_KEY` in the same `chrome.storage.local.get` as the auto-translate keys. A `chrome.storage.onChanged` listener (area `local`) replaces it with `parseSelection(newValue)`.
- `autoTranslateCurrentLecture`: `const target = requestTarget(selection, autoTranslateConfig.provider)`. Spread `target` into the request in place of `provider:`, and put `target.provider/model/effort` into `fullTranslateStatus`.
- `handleCue`: `const target = requestTarget(selection, liveProvider)`. The guard becomes `!liveMode || !target.provider || !extractor`. Spread `target` into the per-cue request.
- `startFullTranscriptTranslate`: copy `request.model` and `request.effort` into `fullTranslateStatus`.

- [ ] **Step 2: Typecheck and run the tests**

Run: `cd extension && npm run typecheck && npm test`
Expected: PASS

- [ ] **Step 3: Commit** — `feat(extension): auto-translate and live mode follow the selected model`

---

### Task 9: Popup model and effort selectors

**Files:**
- Modify: `extension/src/popup/App.tsx`

**Interfaces:**
- Consumes: `DaemonClient.listModels`, `ModelInfo`, and everything in `selection.ts` (Task 7). Also `TranslateStatus.model/effort` (Task 7).

- [ ] **Step 1: Implement**

- **State.**
  - Replace `chosen` with `selection: Selection | null`; the current provider is `selection?.provider ?? ''`.
  - Add `modelLists: Record<string, { state: 'loading' } | { state: 'ok'; models: ModelInfo[] } | { state: 'error'; message: string }>`.
- **On a providers load.**
  - Compute `reconcileProvider(await loadSelection(), names) ?? (names[0] ? withProvider(null, names[0]) : null)`.
  - Then set it and `saveSelection` it.
- **When the current provider changes, and on ⟳.**
  - Call `client.listModels(provider, { refresh })`. A ⟳ click passes `refresh: true`.
  - On success, store the list. If `reconcileChoice(choiceFor(sel, provider), models)` differs from the current choice, apply it with `withChoice` and save.
  - On error, store the message and leave the selection unchanged.
- **Every user change** goes through `withProvider` / `withChoice` and then `saveSelection`. Do not re-send `SET_LIVE_MODE`; the content script follows storage.
- **Layout** (popup is 392 px wide).
  - Row 1 is a `Space.Compact`: provider `Select` (~96 px), model `Select` (flex), effort `Select` (~84 px), and a `Button` with `ReloadOutlined`.
  - Row 2 is the existing Translate button, `block`.
  - The model select shows `loading` while fetching.
- **Model options.**
  - The first option is `''`. Its label is `CLI default` for codex and agy, and `Config default` otherwise. Append ` · <default_model>` when `ProviderInfo.default_model` is non-empty.
  - Then each model as `label === id ? id : \`${label} (${id})\``.
  - If the remembered model is not in the list (still loading, or the fetch failed), add it as an option so it stays visible.
- **The effort select** renders only when the chosen model's `ModelInfo.efforts` is non-empty. Its options are those efforts.
- **Picking a model** applies `defaultChoiceFor(info)`. Picking the default option applies `CLI_DEFAULT`.
- **Error line.** Under the selectors, show `models: <message>` in the existing `providers:` error style.
- **onTranslate.**
  - `request` spreads `requestTarget(selection, provider)`.
  - `autoTranslate.provider` stays the current provider (legacy fallback only).
  - `job` gets `model` and `effort`.
- **Job header.** It shows `[job.provider, job.model, job.effort].filter(Boolean).join(' · ')`. `jobFromTranslateStatus` copies `model` and `effort` from the status.
- **onLiveModeToggle** keeps working off the current provider.

- [ ] **Step 2: Typecheck, test and build**

Run: `cd extension && npm run typecheck && npm test && npm run build`
Expected: all PASS; `dist/` built.

- [ ] **Step 3: Commit** — `feat(popup): pick model and effort from live provider lists`

---

### Task 10: End-to-end smoke test with the real CLIs (no deploy)

**Files:** none committed. Use a scratch config and cache under the session scratchpad.

- [ ] **Step 1: Build a test binary**

Run: `cd daemon && go build -o <scratch>/dualsub ./cmd/dualsub`

- [ ] **Step 2: Start it on a spare port with a scratch config**

The scratch config has `[providers.codex]` (`bin = "/home/johnny/.npm-global/bin/codex"`), `[providers.agy]`, `[cache] path = "<scratch>/cache.db"`, and no `[sync]` section.

Run: `<scratch>/dualsub serve --config <scratch>/config.toml --addr 127.0.0.1:17878` (in the background)

- [ ] **Step 3: Check the endpoints**

- `curl -s 127.0.0.1:17878/v1/providers` → contains `agy` and `codex`, and no `gemini`.
- `curl -s 127.0.0.1:17878/v1/providers/codex/models` → contains `gpt-5.6-sol` with `"default_effort":"low"`, and no `gpt-reserve`.
- `curl -s 127.0.0.1:17878/v1/providers/agy/models` → contains `gemini-3.8-flash-low`.
- `curl -s 127.0.0.1:17878/v1/providers/agy/models?refresh=1` → 200.

- [ ] **Step 4: Translate two real lines with each provider**

- POST `/v1/translate` with codex, `{"model":"gpt-5.6-sol","effort":"low"}`, and lines including `Tom & "Jerry" | 100%`. The stream must end in `done`, with 2 translated lines.
- Repeat with agy and `{"model":"gemini-3.8-flash-low"}`, using new lines so the cache does not answer.

- [ ] **Step 5: Stop the test daemon and delete the scratch files.** The live `dualsub.service` is untouched until the branch is merged; deployment is a finishing step.
