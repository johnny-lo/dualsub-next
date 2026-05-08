# DualSub Next — Codebase Guide for AI Agents

> **Audience: AI sessions, not humans.** This file is a reference index, not a
> tutorial. If you only have time for two sections read §1 (mental model) and
> §3 (file map). Then read §4 (contracts) and §8 (don't-fix list) before
> proposing any change. Verify load-bearing claims by reading the cited files;
> code is truth, this doc decays.

---

## 1. Mental model

A bilingual-subtitle assistant for Netflix and Udemy split into two
deployments that talk over `localhost:7878`:

- **`daemon/`** (Go) — translation orchestrator. Owns LLM provider adapters,
  chunked retrying parallel translation, and the SQLite cache.
- **`extension/`** (TypeScript / React 18 / Antd v5) — Chrome MV3 extension.
  Owns DOM extraction (only the browser can do this), the bilingual overlay
  on the player, popup + options UI.

The daemon is **the** brain for translation reliability; the extension is a
thin client. Anything that can be moved to Go has been; the extension owns
only what JavaScript-in-the-browser uniquely can do.

Communication: HTTP (JSON) for one-shot, SSE for the chunked translate stream.
No auth, no TLS — localhost-only single-user.

## 2. Data flow

```
[Netflix/Udemy player DOM]
   │ extractFullTranscript() / observeCurrentCue()
   ▼
[Content script: extension/src/content/]
   │ chrome.runtime.sendMessage   ◄── popup ↔ content
   ▼
[Popup: extension/src/popup/App.tsx]
   │ POST /v1/translate (JSON)    ◄── extension ↔ daemon
   ▼
[Daemon HTTP: daemon/internal/server/]
   │ translate.Input{Lines, Provider, ...}
   ▼
[Orchestrator: daemon/internal/translate/]
   ├─ cache lookup (line-granular, cache.Key())
   ├─ chunk into N-line batches (default 30)
   └─ worker pool (default 3) → provider.Translate()
                                     │ HTTP to vendor (OpenAI / Gemini / Ollama)
                                     ▼
                                ParseResponse → cache.StoreTranslations
                                     │
                                     ▼ events flow back
[SSE stream: job-created → chunk-done* → chunk-error* → done]
   │
   ▼
[Popup updates progress; pushes per-chunk translations to content script]
[Content script overlay (Shadow DOM) renders bilingual cue on the video]
```

Live mode skips the popup: content script directly calls
`DaemonClient.translate()` with one line per cue as cuechange fires.

## 3. File map

### daemon/

| Path | Purpose |
|---|---|
| `cmd/dualsub/main.go` | Entry. Subcommands: `serve`, `config init`, `version`. Owns wiring: load config → build providers map → make orchestrator → start HTTP server + logger. `buildProviders()` is the single registration point for provider impls. |
| `internal/config/config.go` | TOML load + env-var overrides + defaults + `~` expansion. `Save()` does atomic-rename TOML write. **Both `toml:` and `json:` tags required** on every field — TOML is on disk, JSON is the API. |
| `internal/cache/cache.go` | SQLite (`modernc.org/sqlite`, pure Go). Three tables: `translations`, `transcripts`, `jobs`. `Key()` is the canonical cache-key derivation. `:memory:` accepted for tests. `SetMaxOpenConns(1)` is intentional (see §7). |
| `internal/logger/logger.go` | JSONL append-only with mutex. `New("")` writes only to stderr. No rotation; user wipes manually. |
| `internal/provider/` | LLM adapters. `provider.go` = interface + types + `Error` + error codes. `http.go` = shared client + status→code mapping (`mapStatus`). `prompt.go` = `BuildPrompt` + `ParseResponse`. `openai.go`, `gemini.go`, `ollama.go`, `claude.go` (stub). |
| `internal/translate/` | Orchestrator. `event.go` = typed events. `orchestrator.go` = chunking + worker pool + retry-with-backoff + cache writes. Translation runs the worker pool; `Translate(ctx, in, events chan<- Event)` closes `events` itself. |
| `internal/server/` | HTTP routes + SSE writer + CORS. `server.go` constructs and exposes `ListenAndServe`. `handlers.go` has all route handlers. `sse.go` is the SSE writer. |

### extension/

| Path | Purpose |
|---|---|
| `manifest.config.ts` | MV3 manifest defined in TS via `@crxjs/vite-plugin`. `host_permissions` includes `http://127.0.0.1:7878/*` — needed for content-script fetches to daemon. |
| `vite.config.ts` | CRXJS plugin builds the extension; `@/` alias = `src/`. |
| `src/background/index.ts` | SW; only logs lifecycle today. |
| `src/content/index.ts` | Message router. Receives: `EXTRACT_TRANSCRIPT`, `SET_OVERLAY`, `PATCH_OVERLAY`, `CLEAR_OVERLAY`, `SET_LIVE_MODE`, `PING_OVERLAY`. Holds module-scoped state for overlay + Live mode (intentionally not chrome.storage — re-translate after page reload). |
| `src/content/extractors/types.ts` | `SubtitleExtractor` interface, `SiteId` union, `ExtractError`. |
| `src/content/extractors/index.ts` | `detectSite()` factory — single registration point for new sites. |
| `src/content/extractors/NetflixExtractor.ts` | Netflix extractor. `video.textTracks` for both extraction and observation. |
| `src/content/extractors/UdemyExtractor.ts` | Udemy extractor. **Tier 1 REST API for extraction** (Tier 2 textTrack as fallback only). **`observeUdemyCaptions` polls `[class*="well--text"]` every 250ms for live observation** — Udemy's [CC] track does not populate `video.textTracks`. |
| `src/content/extractors/cueObserver.ts` | TextTrack-based cue subscription. Used by Netflix; **NOT used by Udemy** (Udemy uses DOM polling — see §7). Emits `ActiveCue { texts: string[], ... }` so multiple simultaneous cues stay distinct (cache keys are per-cue). |
| `src/content/extractors/textTrack.ts` | Helper: `waitForCues` + `cuesToEntries` for full-transcript bulk extraction. |
| `src/content/extractors/parseVtt.ts` | VTT parser used by Udemy Tier 1. |
| `src/content/overlay/SubtitleOverlay.ts` | Shadow-DOM bilingual overlay. Drag-to-reposition. `setTranslations()` replaces, `patchTranslations()` merges. `render(texts: string[] \| null)` builds one `(original, translated)` pair per active cue; lookup uses `normalizeText()`. |
| `src/popup/App.tsx` | Main UI. Daemon status, provider dropdown, Translate flow with per-chunk overlay updates, Live mode switch, recent jobs, Extract & Copy fallback. |
| `src/options/App.tsx` | Antd Form covering server / translate / cache / 3 providers. Saves via `client.putConfig()`; UI tells user to restart daemon. |
| `src/shared/DaemonClient.ts` | One class wrapping daemon HTTP + SSE. `translate(req, handlers)` returns an abort fn. |
| `src/shared/messaging.ts` | `ContentMessage` discriminated union + `sendToActiveTab()`. **All popup ↔ content message types live here**, nowhere else. |
| `src/shared/transcript.ts` | `TranscriptEntry`, `formatTranscriptForExport`, `normalizeText`, `parseTranslatedTranscript`. |

## 4. Critical contracts (cross-file invariants)

These are the rules that span multiple files and break silently if you forget.

### 4.1 `provider.Provider` interface

```go
type Provider interface {
    Name() string
    Translate(ctx context.Context, in Request) (Response, error)
}
```

All provider errors should be `*provider.Error` so `IsRetryable()` works; the
orchestrator dispatches retry decisions based on `pe.Retryable`.

Error codes are constants in `provider.go`. Don't invent new ones without
adding to that list and updating `mapStatus` if HTTP-driven:

```
PROVIDER_RATE_LIMIT     PROVIDER_INVALID_KEY    PROVIDER_TIMEOUT
PROVIDER_SERVER_ERROR   PROVIDER_BAD_REQUEST    PROVIDER_CONTEXT_TOO_LONG
PARSE_FAILED            NETWORK                 NOT_IMPLEMENTED
MISSING_CONFIG
```

### 4.2 The `[N]` prompt protocol

LLMs are asked to translate lines prefixed by `[N]` and emit translations in
the same form. `BuildPrompt` produces the prompt; `ParseResponse` extracts
the result. Missing indices → `PARSE_FAILED` (retryable). LLMs frequently
add commentary around the lines; the parser tolerates it.

The line `Index` is the **stable identifier** end-to-end:
- Orchestrator chunks preserve indices (no renumbering).
- Cache rows store original/translated text; cache key derives from text,
  not index, so the same text across videos shares a cache row.

### 4.3 SSE event protocol

Defined in `daemon/internal/translate/event.go` and mirrored in
`extension/src/shared/DaemonClient.ts`. **Names are string-literal-equal on
both sides** — the TS dispatcher is a string switch.

| Event | When | Payload |
|---|---|---|
| `job-created` | once at start | `{job_id, total_chunks, total_lines, cache_hits}` |
| `chunk-done` | per chunk success — incl. cache batch as `chunk=0`/`source="cache"` | `{chunk, source, lines}` |
| `chunk-error` | per attempt; `final=true` only on terminal failure | `{chunk, code, message, retryable, attempt, final}` |
| `done` | once at end | `{job_id, total, completed, failed, cache_hits}` |

### 4.4 Cache key (`cache.Key`)

```
sha256(provider | model | sourceLang | targetLang | normalize(originalText))
```

`normalize` lowercases and collapses whitespace. Always derive cache keys
via `cache.Key()`; never recompute by hand. Including `model` is
intentional — different models give different translations, so swapping
the model in config invalidates cache entries (no migration).

### 4.5 Extension ↔ content script messages

Defined once in `src/shared/messaging.ts`. Content listener returns `true`
only for paths that need an async `sendResponse` (currently
`EXTRACT_TRANSCRIPT`). All others are sync. To add a message type:

1. Add to the `ContentMessage` union.
2. Handle in `src/content/index.ts`.
3. Call from popup via `sendToActiveTab<R>()`.

### 4.6 Provider config — TOML AND JSON

Every field in `internal/config/config.go` carries both tags:

```go
ChunkSize int `toml:"chunk_size" json:"chunk_size"`
```

Drop one and you'll silently break either disk persistence or the HTTP API.

## 5. Conventions

- **Comments**: minimal. Document the WHY for non-obvious code; never the
  WHAT. The repo deliberately has no docstrings on simple methods.
- **Errors**: provider errors are typed (`*provider.Error`); HTTP handlers
  return JSON or plain text via `http.Error`. Orchestrator splits errors:
  setup errors return from `Translate()`; runtime failures are events.
- **Tests**: `:memory:` SQLite + mock provider for orchestrator.
  `httptest.NewServer` + mock provider for HTTP routes. Live API tests are
  gated by `//go:build integration` and `*_API_KEY` env vars. **34 tests
  total** across daemon (cache 7 + config 5 + logger 2 + provider 7 + server
  6 + translate 7).
- **Concurrency**: SQLite uses `SetMaxOpenConns(1)`; do not parallelise
  writes. Orchestrator parallelism is `Config.Concurrency` (default 3).
  Provider HTTP timeout is 90s default; 5min for Ollama (cold model load).
- **Pure Go SQLite**: `modernc.org/sqlite` (driver name is `sqlite`, NOT
  `sqlite3`). Don't switch to `mattn/go-sqlite3` — we want CGO-free builds
  for cross-platform binary distribution.
- **Antd v5**: extension uses Antd v5's CSS-in-JS. The injected overlay uses
  vanilla Shadow DOM, NOT Antd, to keep content-script bundle small.
- **Module path**: `github.com/johnny/dualsub-next/daemon`. Internal packages
  stay under `internal/`.

## 6. Extension points

### Add a new LLM provider

1. Implement `provider.Provider` in `daemon/internal/provider/<name>.go`.
   Use `gemini.go` as the template — it covers the trickiest cases (URL
   key, native request shape, multi-part response).
2. Add a `<Name>Provider` config struct in `daemon/internal/config/config.go`
   and include it in `ProvidersConfig`.
3. Register in `daemon/cmd/dualsub/main.go:buildProviders`.
4. Surface in `EnabledProviders()` (skips if no API key required, e.g. Ollama).
5. Tests: add a `Test<Name>Live` to `provider/integration_test.go` gated by
   env var. Optional: add to the Options form in
   `extension/src/options/App.tsx`.

### Add a new site (YouTube, Coursera, …)

1. Implement `SubtitleExtractor` in
   `extension/src/content/extractors/<Name>Extractor.ts`. Reuse
   `observeViaTextTracks` if the site exposes textTracks; otherwise write
   a custom DOM-mutation observer.
2. Register in `extension/src/content/extractors/index.ts:detectSite`.
3. Update `manifest.config.ts`: add to `content_scripts.matches` and
   `host_permissions`.
4. Update `SiteId` union in `extension/src/content/extractors/types.ts`.

### Add a new SSE event type

1. Add constant + payload struct in `daemon/internal/translate/event.go`.
2. Emit from orchestrator at the right point.
3. Mirror name + payload TS interface in `extension/src/shared/DaemonClient.ts`.
4. Add handler field to `TranslateHandlers`; dispatch in `dispatch()`.

### Change cache schema

There is no migration story. Either:
- Bump the cache key (e.g., add a version prefix in `cache.Key`) so old rows
  miss, or
- Document a "delete `~/.local/share/dualsub/cache.db` on upgrade" step.

## 7. Gotchas (non-obvious things that bite)

- **Netflix has no transcript API**. The extractor only works if the user
  has enabled subtitles in the Netflix UI first (so `video.textTracks`
  populates). Surface this clearly when `NO_TEXT_TRACKS` fires.
- **Udemy course-id discovery is fragile**. The extractor tries 4 different
  ways to find the course ID (data attributes, body HTML regex, script tag
  scan); vendor changes break this often. See `getCourseIdFromPage`.
- **MV3 host_permissions and ports**: `http://127.0.0.1/*` does NOT match
  port 7878. Always include the port in `manifest.config.ts`.
- **Content-script fetch + CORS**: daemon CORS is wildcard so
  chrome-extension origins work. Don't tighten this without thinking
  through who calls what.
- **modernc.org/sqlite single-conn**: `SetMaxOpenConns(1)` is intentional;
  otherwise you'll see `database is locked` under any concurrency.
- **Gemini API key in URL**: traditional Gemini auth puts the key in the
  query string, not a header (see `gemini.go`). Be careful logging request
  URLs — they leak the key.
- **PUT /v1/config does NOT hot-reload** the running daemon. It writes the
  file and the response says "restart daemon to apply". Intentional.
- **`onChunkError` events with `final=false`** are intermediate retry
  attempts. Don't render them as failures in UI; only `final=true` matters
  for the user.
- **Cache hit goes through `chunk-done`** (with `chunk=0`, `source="cache"`),
  NOT a separate event. Counted separately in the `done` payload's
  `cache_hits`.
- **Async `sendResponse` in content script**: must `return true` from the
  listener for the channel to stay open. Returning `false` for sync handlers
  is correct; mixing them up causes silent message drops.
- **`ParseResponse` is retryable**: `PARSE_FAILED` has `Retryable: true`
  because LLM nondeterminism may produce a clean parse next time. Don't
  flip this without thinking through chunk-level cost.
- **Module-scoped state in content script**: overlay + Live mode state
  doesn't survive page reload (intentional — re-translate is fine for
  self-use; chrome.storage adds churn). Don't "fix" this.
- **Udemy [CC] is not in `video.textTracks`**. The Udemy player renders
  [CC] caption text into a DOM element matching `[class*="well--text"]` and
  never populates the HTML5 TextTrack API for it. `UdemyExtractor` therefore
  uses `observeUdemyCaptions` (250 ms DOM poll) for live observation, and
  `extractFullTranscript` prefers Tier 1 REST API. Tier 2 / `observeViaTextTracks`
  works on Netflix only. [自動] tracks may sometimes populate textTracks, but
  routing everything through DOM keeps extraction and observation aligned.
- **Udemy SPA lecture switching swaps the `<video>` element**. `startCueObserver`
  in `content/index.ts` re-attaches every time it is called — do not re-add a
  "skip if disposer exists" guard.

## 8. Things deliberately NOT done — do not "fix"

These are decisions, not omissions. Each was discussed with the user.

- **Claude provider**: stub returning `NOT_IMPLEMENTED`. Reason: user has
  no Anthropic API credit (only ChatGPT/Claude.ai subscriptions). Do not
  add reverse-engineered Claude.ai cookie auth.
- **Token-level streaming**: explicitly rejected in design. Chunks are the
  unit of progress. SSE per-token would force per-provider stream parsers
  and doesn't solve the user's actual pain (translation reliability).
- **Daemon auto-start / tray icon / GUI**: it's a CLI; user runs
  `./dualsub serve` manually. No systemd / launchd / tray integration.
- **Log rotation**: `logger.go` is append-only. User wipes manually.
- **Config hot-reload on PUT**: see §7.
- **Auth between extension and daemon**: localhost-only, single user. CORS
  wildcard is fine.
- **Chrome extension icons**: not provided; falls back to default puzzle
  piece. Don't generate placeholder icons.
- **Floating control panel on the page**: popup is the control plane. Don't
  add in-page widgets.
- **vitest / extension tests**: not wired. `npm run build` (which includes
  `tsc --noEmit`) is the bar. Don't propose adding a test framework to the
  extension unless the user asks.
- **`max_tokens` / `num_predict` in provider requests**: deliberately not
  set — let model defaults apply. Setting them caused truncation in the
  earlier codebase.

## 9. Build / verify

```bash
# daemon: vet + all tests
cd daemon && go vet ./... && go test ./...
# expect: 34 tests across cache(7) config(5) logger(2) provider(7) server(6) translate(7)

# daemon: live integration (real keys; auto-skipped without env)
GEMINI_API_KEY=...   go test -tags=integration ./internal/provider/ -run TestGeminiLive -v
OPENAI_API_KEY=...   go test -tags=integration ./internal/provider/ -run TestOpenAILive -v
OLLAMA_TEST=1        go test -tags=integration ./internal/provider/ -run TestOllamaLive -v

# extension: typecheck + bundle (no test framework)
cd extension && npm run build
# expect: tsc --noEmit passes; vite emits dist/ with valid manifest.json

# end-to-end smoke: requires real provider key + a Udemy/Netflix page (manual)
```

## 10. Change checklist (read before opening an edit)

If you change … then check …

- A field in `internal/config/config.go` → both `toml:` and `json:` tags
  present; HTTP `/v1/config` GET/PUT round-trips; existing `config.toml`
  files still load.
- An SSE event payload → mirror struct in `DaemonClient.ts`; dispatcher
  string switch covers it; popup state machine handles new fields.
- The cache schema → migration plan (or doc deletion of `cache.db`); cache
  key derivation if semantics shift.
- A provider's request/response shape → its parser; the unit test in
  `provider_test.go`; the integration test if applicable.
- The `manifest.config.ts` permissions → user re-consent on Chrome reload;
  content-script fetch targets all listed in `host_permissions`.
- The `ContentMessage` union → the listener in `content/index.ts`; every
  `sendToActiveTab` callsite that uses the changed type.
- The `provider.Provider` interface → all 4 impls; the mock in
  `translate/orchestrator_test.go` and `server/server_test.go`.

## 11. Glossary

- **chunk** — a batch of subtitle lines sent in one LLM call. Default 30.
- **cache hit** — a line whose translation was already in SQLite for the
  same `(provider, model, src, tgt, text)`. Surfaces as `chunk-done`
  with `source="cache"`, `chunk=0`.
- **Live mode** — content-script-driven per-cue translation as the video
  plays. Uses the same `/v1/translate` with one line per request.
- **batch translate** — popup-driven translation of the full transcript at
  once. The main flow.
- **TextTrack** — the HTML5 API exposing video captions. Both extractors
  use it (Udemy as Tier 2, Netflix as Tier 1). Only works after the user
  enables subtitles in the player.
- **PARSE_FAILED** — the LLM's response did not contain `[N]` lines for
  every input index. Retryable; the orchestrator will attempt again with
  exponential backoff.
- **Tier 1 / Tier 2** (Udemy) — Tier 1 = Udemy REST API → VTT URL → fetch
  → parse. Tier 2 = HTML5 TextTrack fallback. **Tier 1 is the primary path**
  (TextTrack is unreliable on Udemy — see §7). Live observation goes through
  DOM polling (`observeUdemyCaptions`) on `[class*="well--text"]` regardless
  of which extraction tier succeeded.
