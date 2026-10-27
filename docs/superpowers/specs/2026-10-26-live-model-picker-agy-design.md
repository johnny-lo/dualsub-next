# Design: live model picker, and agy replaces the Gemini API provider

Date: 2026-10-26

## Problem

The popup lets the user pick a translation *provider* but not a *model*. For
codex, dualsub's `[providers.codex]` leaves `model` empty, so every
translation runs on whatever `~/.codex/config.toml` says. On the user's
machine that is `gpt-6-astra`, which is too expensive for subtitle work. The
user wants `gpt-5.6-sol` at `low` reasoning effort, which is fast and good
enough. Today they cannot pick it, and they cannot set the effort from dualsub
at all.

The user does not want a hardcoded model list. What a subscription can use
changes over time, so the list must come from the live account each time.

Separately, the user wants Gemini to go through their Google Antigravity
subscription (the `agy` CLI) instead of a Gemini API key. The API-key Gemini
provider is removed entirely.

## Goals

1. The popup gets a **model** selector next to the provider selector. For
   codex it also gets an **effort** selector.
2. The model list is fetched live from each provider, never hardcoded:
   - codex: `codex debug models`
   - agy: `agy models`
   - OpenAI: `GET {base_url}/models`
   - Ollama: `GET {base_url}/api/tags`
3. The chosen provider, model and effort are remembered. The next time the
   popup opens, they are preselected. Full-transcript translate, Udemy
   auto-translate of later lectures, and live mode all use the same choice.
4. A new `agy` provider translates by running the local `agy` CLI in print
   mode. It uses the user's existing agy login and needs no API key.
5. The API-key Gemini provider is removed from the daemon, the options page,
   the config template and the docs.
6. All of this works, and is rolled out, on the user's Windows machines as
   well as the Linux one (the user's decision).

## Non-goals

- An effort selector for agy, OpenAI or Ollama. agy's Gemini models carry the
  effort in the model name (`gemini-3.8-flash-low`, `-medium`, `-high`), so
  picking the model already picks the effort.
- A model or effort selector on the options page. The selection lives in the
  popup. The options page's existing "Default model" fields for OpenAI and
  Ollama stay as they are, as a fallback.
- Settings-page UI for agy. Like codex, agy is configured in TOML only.
- Showing the shared-cache central node's model list. The popup lists the
  models that *this* daemon's CLIs report (see "Shared cache" below).
- Cost or quota display.
- Re-translating lines that were already translated with another model. The
  cache key is already provider- and model-independent (`cache.Key` ignores
  both), and that stays.

## Anti-goals (failure even if it "works")

- Any hardcoded model list, in the extension or in the daemon. (The existing
  single fallback defaults for OpenAI, `gpt-4o-mini`, and Ollama,
  `qwen2.5:7b`, stay as they are; no new ones are added.)
- An agy call that can change files or run commands. Translation is one prompt
  in and text out.
- A failed model listing (not logged in, network down, CLI missing) that blocks
  translation. The user can always translate with the CLI's own default.

## What the user sees

Popup, translate section, left to right:

```
[ Provider ▾ ]  [ Model ▾ ]  [ Effort ▾ ]  (⟳)   [ Translate ]
```

- **Model** options:
  - The first option is always "CLI default" (codex/agy) or "Config default"
    (OpenAI/Ollama), which sends no model and lets the CLI or config decide.
    For codex on the user's machine that means `gpt-6-astra`.
  - Then the live list, labelled with the provider's display name and showing
    the id when it differs. Example for codex: "GPT-5.6-Sol (gpt-5.6-sol)".
- **Effort** shows only for codex, and only when a specific model is chosen.
  - Its options are that model's `supported_reasoning_levels`.
  - It preselects the model's `default_reasoning_level`. For `gpt-5.6-sol`
    that is `low`.
  - When the model is "CLI default", the effort selector is hidden and no
    effort is sent.
- **⟳** re-fetches the list for the current provider, bypassing the daemon's
  cache.
- While the list loads, the model selector shows a loading state. The
  remembered model stays selected.
- If listing fails, a one-line error appears under the selectors, like the
  existing "providers: …" error line. Translation still works with "CLI
  default" or with the remembered model.
- The current job line shows `provider · model · effort` instead of only the
  provider. When the CLI default was used, it shows just the provider.

First use: nothing is remembered, so the popup shows the first provider and
"CLI default". After the user picks codex, `gpt-5.6-sol` and `low`, the popup
reopens with exactly that.

## How it works

### Daemon: listing models

New endpoint: `GET /v1/providers/{name}/models[?refresh=1]`.

```json
{
  "models": [
    {
      "id": "gpt-5.6-sol",
      "label": "GPT-5.6-Sol",
      "efforts": ["low", "medium", "high", "xhigh", "max", "ultra"],
      "default_effort": "low"
    }
  ],
  "fetched_at": "2026-10-26T11:22:26Z"
}
```

- The `efforts` and `default_effort` fields are omitted for providers without
  effort.
- An unknown provider returns 404.
- A listing failure returns 502 with `{ "error": "<message>" }`. Error text
  goes through the existing redaction (`sanitizeMessage`).

Providers that can list models implement an optional interface, for example
`ModelLister { ListModels(ctx) ([]ModelInfo, error) }`. The handler
type-asserts for it. `sharedcache.FallbackProvider` wraps local providers on a
client node, so it must forward `ListModels` to the provider it wraps. Without
that, the client node would lose model listing.

Results are cached in memory per provider for **5 minutes**. `refresh=1`
bypasses and replaces that cache. Failures are not cached.

Per-provider sources (all verified on the user's Linux machine on 2026-10-26):

| Provider | Command / request | Parse | Effort |
|---|---|---|---|
| codex | `codex debug models` (refreshes from the account; ~0.6 s). Use the configured `bin`. | JSON `models[]`: keep `visibility == "list"`, sort by `priority`, id = `slug`, label = `display_name` | `supported_reasoning_levels[].effort`, `default_reasoning_level` |
| agy | `agy models` (~1.6 s). Use the configured `bin`. | stdout lines `id<TAB>label`; the "Fetching available models..." line goes to stderr. stdout or stderr containing `Please sign in` → error "agy is not signed in" | none |
| openai | `GET {base_url}/models`, bearer key | `data[].id`; drop ids that are clearly not text-generation (embedding, tts, whisper, dall-e, image, audio, realtime, moderation, transcribe) | none |
| ollama | `GET {base_url}/api/tags` | `models[].name` | none |

Today the codex catalog lists `gpt-6-astra`, `gpt-5.6-sol`, `gpt-5.6-terra`
and `gpt-5.6-luna`. The hidden ones (`gpt-reserve`, `codex-auto-review`) are
filtered out.

### Daemon: translating with a model and effort

- `POST /v1/translate` gains `effort` (string, optional), alongside the
  existing `model`.
- The effort flows through `translate.Input` and then `provider.Request`
  (new `Effort` field). Providers that do not support effort ignore it.
- codex: when `effort` is non-empty, add `-c model_reasoning_effort=<effort>`
  to `codex exec`. Model handling (`-m`) is unchanged.
- The daemon does not validate model or effort against the list. A stale or
  wrong value fails at the CLI with its own error, which reaches the popup
  through the existing error path.

### Shared cache

On a client node (`sync.central_url` set), translation goes through
`FallbackProvider` → `Client.Resolve`, which already forwards `Model` to the
central node.

- Add `effort` (omitempty) to the resolve request in `sharedcache/protocol.go`
  and pass it through on both sides.
- An older central node ignores the field and uses its default effort. That is
  acceptable.
- Known limitation: the popup lists the client node's models, and the central
  node may not have the same ones. In that case the central's CLI error comes
  back as a translation error. Not solved here.

The user's Linux machine is the central node (`sync.listen` set).

### agy provider

New `provider/agy.go`, modelled on `codex.go`.

Config:

```toml
[providers.agy]
bin = "agy"     # optional; default resolves "agy" on PATH
model = ""      # optional default model, e.g. "gemini-3.8-flash-low"
```

- It is enabled when the section exists, like codex.
- `DefaultModel()` returns `model`.
- Add it to `EnabledProviders`, `buildProviders` and the config template.

Translate call, as verified:

```
agy -p <prompt> --model <model> --output-format json --print-timeout <timeout+60s>
```

- `--model` is passed only when a model is set. Without it, agy uses its own
  default.
- stdout is one JSON object:
  `{"status":"SUCCESS","response":"<text>","conversation_id":…,"usage":{…}}`.
- Feed `response` to the existing `ParseResponse`. A status other than
  `SUCCESS` is an error.
- The prompt is the same system+user text that codex gets (`BuildPrompt`).
- Run in a dedicated empty working directory: a fresh temp dir per call,
  removed afterwards. agy treats the working directory as its workspace.
- Never pass `--dangerously-skip-permissions`. In print (headless) mode, agy
  automatically denies any tool that needs approval, such as writing files or
  running commands. Tools that need no approval, such as reading files, only
  see the empty temp dir. That is the whole tool lock.
  - No PreToolUse deny-all hook (Thoth's `.agents/hooks.json` approach). This
    is Claude's call: the hook adds Windows quoting pitfalls for little gain.
  - The builder must still confirm that agy does not hang waiting for a
    prompt; the fake-CLI tests cannot prove this.
- Error mapping, mirroring codex:

  | Case | Code |
  |---|---|
  | `Please sign in` | `CodeMissingConfig`, not retryable |
  | `is not recognized` (unknown model) | `CodeBadRequest`, not retryable |
  | rate limit / quota / 429 text | `CodeRateLimit`, retryable |
  | timeout | `CodeTimeout`, retryable |
  | other non-zero exit | `CodeServerError`, retryable |

- Timeout: 4 minutes, like codex.
- Every `agy -p` call leaves a conversation in the user's agy history:
  roughly 10 per lecture, and one per cue in live mode. agy has no flag to
  stop this.
  - The user accepted the build-up. dualsub does not delete or touch agy's
    data directory.
  - agy stays available in live mode.

Prompt size: Thoth found that agy's `-p` takes the next argv token as the
prompt, and that `-p -` is a literal "-", so the prompt goes on argv. With the
user's `chunk_size = 100`, a chunk is roughly 10 KB, well under Linux's 128 KB
per-argument limit and Windows' 32 K-character command line. The daemon also
runs on Windows (`dualsub-bg.ps1`), so before launching, the provider must
fail cleanly with `CodeContextTooLong` if the prompt would exceed a safe argv
size (about 24 K characters). The alternative is `--input-format stream-json`
on stdin, which also requires `--output-format stream-json`. The builder may
use that instead if it proves reliable.

Windows launch: subtitle text is untrusted and can contain `&`, `|`, `%`,
`"` and line breaks.
- If `agy` on Windows resolves to a `.cmd` or `.bat` shim rather than an
  `.exe`, the prompt must never go on its argv, because `cmd.exe` would
  reinterpret it (Thoth refuses this for the same reason: "BatBadBut").
- In that case, use the stdin `stream-json` route, or refuse with a clear
  `CodeMissingConfig` message.
- The builder checks what the Windows agy install actually is before choosing.

Reference implementation: Thoth's `src/adapter/antigravity/mod.rs`
(`build_command`, `auth_command`, `parse_auth_output`), at
`~/Desktop/Thoth`.

### Removing the Gemini API provider

Delete:
- `provider/gemini.go`
- `TestGeminiLive`
- `config.GeminiProvider` and the `Providers.Gemini` field
- the `GEMINI_API_KEY` env override, the `gemini` branch in `EnabledProviders`
  and `buildProviders`, and the Gemini block in the config template
- the Gemini card on the options page, and `gemini` in `DaemonClient`'s config
  type
- the Gemini mentions in `README.md`, `codebase.md` and the `dualsub help`
  text

Change the "no providers configured" example in `runServe` to show
`[providers.codex]` or `[providers.agy]`.

Existing config files that still have `[providers.gemini]` keep loading,
because `toml.Unmarshal` ignores unknown keys. The next save from the options
page drops the section. That is intended.

Keep the API-key redaction in `http.go` and its test, as a general defence.
They are not Gemini-specific in effect. Test fixtures that only use `"gemini"`
as an arbitrary provider-name string (cache, sharedcache, server tests) may
stay or be renamed; that is up to the builder.

### Extension

- `DaemonClient`:
  - add `listModels(provider, { refresh })`
  - `TranslateRequest` gains `effort?: string`
- Popup: keep the selection in `chrome.storage.local` under one key, for
  example
  `dualsubSelection = { provider, perProvider: { [name]: { model, effort } } }`.
  - Each provider remembers its own last model and effort, so switching
    provider and back restores it.
  - Restore it on open.
  - If the remembered provider is no longer offered, fall back to the first
    one.
  - If the remembered model is missing from a *successfully* fetched list,
    fall back to "CLI default". A failed fetch never clears the remembered
    model.
- The model and effort are sent on every path that carries a provider today:
  - `START_TRANSLATE` request
  - Udemy auto-translate of later lectures
  - live mode's per-cue request in `content/index.ts`
- Auto-translate and live mode follow the **current** selection, not a
  snapshot taken when Translate was pressed or live mode was switched on. If
  the user switches codex to `gpt-5.6-sol` / `low` after translating lecture 1,
  lecture 2 is auto-translated with sol.
  - The content script reads provider, model and effort from
    `dualsubSelection` in `chrome.storage.local` each time it starts a
    request.
  - It watches `chrome.storage.onChanged` to keep an in-memory copy current.
  - `AutoTranslateConfig` and `SET_LIVE_MODE` keep only what they mean beyond
    the selection: on/off and the languages.
  - Do not re-send `SET_OVERLAY` to propagate a selection change. It replaces
    the video's stored translations.
- Upgrade path: if `dualsubSelection` does not exist yet, the content script
  falls back to the provider in a pre-existing `AutoTranslateConfig`, with no
  model and no effort.
  - A legacy `"gemini"` there fails once per lecture, through the existing
    attempt-marker path.
  - The first popup open writes `dualsubSelection`, and from then on the
    fallback is no longer used.

## Testing

- Tests never call the real `codex` or `agy`. Use a fake CLI, as
  `codex_test.go` already does: the test binary re-executes itself via
  `TestMain` and an env var, which works on Windows too. Extend it to fake
  `codex debug models`, `agy models` and `agy -p … --output-format json`.
- Daemon:
  - listing parsers for each provider (including the hidden-model filter, the
    agy "Please sign in" case, and the OpenAI non-text filter)
  - the models endpoint (404, 502, the cache TTL and `refresh=1`)
  - codex `-c model_reasoning_effort=` args
  - agy args, JSON parsing and the error mapping
  - effort forwarded through the shared-cache resolve
  - `FallbackProvider` forwarding `ListModels`
- Extension: selection persistence and fallback (missing provider, missing
  model, failed fetch), and the request payloads carrying model and effort.
- Manual check on Linux and on each Windows machine that runs dualsub:
  - codex with `gpt-5.6-sol` + `low` translates a real lecture
  - agy with `gemini-3.8-flash-low` translates a real lecture
  - ⟳ re-fetches the list
- One extra check on Windows: a cue containing `&`, `%`, `"` and a line break
  translates correctly through agy.

## Rollout

Linux (the central node):

- The daemon runs as the `dualsub.service` systemd user unit. Rebuild the
  binary, then run `systemctl --user restart dualsub`; don't `pkill`.
- Add `[providers.agy]` to `~/.config/dualsub/config.toml`. Leave the old
  `[providers.gemini]` section; it is ignored.
- Rebuild the extension, then reload it in Chrome.

Each Windows machine that runs dualsub. This machine cannot reach them, so
the user or a Claude session on that machine does this:

- Before upgrading, make sure agy is installed there and signed in (run
  `agy models` once), and add `[providers.agy]` to its config
  (`%USERPROFILE%\.config\dualsub\config.toml`). If its config has only
  `[providers.gemini]`, the upgraded daemon would otherwise refuse to start
  with "no providers configured".
- Rebuild `dualsub.exe` (`cd daemon ; go build -o ..\dualsub.exe .\cmd\dualsub`).
  Then run `.\dualsub-bg.ps1 stop` and `.\dualsub-bg.ps1 start`.
- Rebuild and reload the extension.
- On a Windows client node, translation goes to the Linux central first, and
  the central runs its own agy or codex. The Windows machine's own agy is used
  for listing models and as the fallback when the central is unreachable.

## Decisions made by the user

- A model selector everywhere a provider is chosen, plus effort for codex.
  Lists are live, never hardcoded.
- Model listing for every configured provider, not only codex.
- agy replaces the Gemini API provider entirely.
- Windows is in scope.
- agy conversations are allowed to build up in the user's agy history.

## Decisions made by Claude (not explicitly by the user)

- A list-models endpoint per provider, with a 5-minute in-memory cache and a
  refresh button.
- A "CLI default" option that is always present, and is the first-use default.
- Each provider remembers its own model and effort.
- No effort selector for agy, OpenAI or Ollama.
- agy is configured in TOML only, with no options-page UI.
- OpenAI's list is lightly filtered to text models.
- The daemon does not validate model or effort before calling the CLI.
- Auto-translate and live mode read the current selection from storage
  instead of a snapshot.
- agy's tool lock is headless auto-deny plus an empty temp dir, with no hook.
- OpenAI's and Ollama's existing fallback default models stay.

## Left to the builder

- The exact Go and TS type and function names.
- Whether recent jobs (`/v1/jobs`) store and show effort. Showing the model
  there is enough.
- How a too-long agy prompt is handled: refuse it, or switch to stdin
  `stream-json`.
- The precise loading and error styling in the popup, consistent with the
  existing Antd components.
