# Cache-only prefetch on page load

Date: 2026-09-21
Status: approved, not yet implemented

## Problem

Nothing is looked up until the user clicks Translate. A lecture whose lines are
already translated — by an earlier session, by another provider, or on another
machine that uploaded to the central node — still shows no subtitles until a
click. The only path into the cache today is `POST /v1/translate`, which
translates whatever it misses, so it cannot be used to answer the cheap
question "do we already have this?".

## Goals

- On page load (and Udemy SPA lecture switch), find existing translations for
  the current lecture and display them, without translating anything.
- Look in the local daemon cache **and** in the central node's cache.
- Never spend provider tokens as a side effect of loading a page.

## Non-goals

- Spending tokens on lines the cache already has. Sticky auto-translate still
  only runs when the user opted in, and `/v1/translate` already skips cached
  lines per line (see "Auto-translate after a partial hit").
- Looking up legacy language-pair spellings. The cache holds 1592 older
  `en → zh-TW` rows next to 9363 `en → 繁體中文` rows; only the current pair is
  queried.
- Netflix-specific work. Prefetch runs wherever `extractFullTranscript()`
  already works; Udemy is the tested path.

## Design

### 1. Local daemon: `POST /v1/lookup`

Cache-only JSON (not SSE). No provider call, no job row, no `sync_outbox`
write for what it finds.

```
→ { source_lang, target_lang, lines: [{index, text}], include_remote: bool }
← { translations: [{index, text}], hits, total, remote_hits, remote_status }
```

- Keys: `cache.Key("", "", source_lang, target_lang, text)`. The shared-v2 key
  is provider-independent, so a lecture translated with gemini is found while
  the user is on codex.
- `lines` is capped at 2000 per request; over the cap is `400`.
- `remote_status` is one of `disabled` (no central configured), `ok`,
  `unsupported` (central too old), `unavailable` (network/central failure), so
  a miss is always distinguishable from a failure.
- Translations found on the central are written to the local cache so the next
  load needs no network. They are stored **without** queueing them in
  `sync_outbox` — they came from the central, so re-uploading is pointless.
  This needs a cache write path that skips the outbox (`StoreTranslations`
  already does; `StoreTranslationsForSync` is the queueing one).

### 2. Central: authenticated `POST /v1/lookup`

Same request shape as `/v1/resolve` minus translation: derive the keys, return
the hits, never touch a provider.

```
→ { source_lang, target_lang, lines: [{index, text}] }
← { translations: { cache_key: text }, cache_hits }
```

Keeps resolve's legacy-key aliasing so a mid-rollout client still matches.
Requires the bearer token like every other route on that listener.

### 3. Shared-cache client: `Client.Lookup()`

- `404` → `ErrLookupUnsupported`, treated by the caller as "no remote hits",
  and **does not open the circuit breaker**. An older central must degrade to a
  miss, never to an error state that would also affect translation fallback.
- A network error does open the circuit breaker, so translation fallback stays
  fast while the central is down.
- Request timeout is seconds, not the 6-minute translate timeout: this is a
  cache read.

### 4. Extension

- New `src/shared/langs.ts` exports `PREFERRED_SOURCE = 'en'` and
  `TARGET_LANG = '繁體中文'`, moved out of `popup/App.tsx`. Both the popup and
  the content script import them, so prefetch keys always match what translate
  wrote. Getting this wrong silently misses every row.
- `DaemonClient.lookup()`: one-shot POST. Called directly from the content
  script — `http://127.0.0.1:7878/*` is in `host_permissions` and the daemon
  sends `Access-Control-Allow-Origin: *`. The background relay exists to keep
  SSE streams alive across MV3 churn; a single POST does not need it.
- `prefetchCachedTranslations(videoKey)` in `src/content/index.ts`:
  1. extract the transcript (`extractFullTranscript(PREFERRED_SOURCE)`),
  2. `POST /v1/lookup` with `include_remote: true`,
  3. on hits: `patchTranslations()`, `startCueObserver()`, merge into
     `chrome.storage.local` via the existing serialized write chain,
  4. never start a translation.
- Order on load: restore from storage (instant) → prefetch fills gaps → sticky
  auto-translate if configured **and the lecture is not fully covered**.
- One prefetch in flight per `videoKey`; dropped on SPA navigation away.
- Extraction failure or daemon offline: log and no-op. Prefetch must never
  break the overlay or the existing flows.

## Data flow

```
page load / SPA nav
  → restoreOverlayFromStorage()            (existing, instant)
  → prefetchCachedTranslations()           (new)
        extractFullTranscript()
        POST /v1/lookup  ──► local cache hit?
                          └─► miss + central configured
                                 └─► central POST /v1/lookup (cache-only)
                                        └─► hits stored locally, no outbox
        hits → overlay + chrome.storage.local
  → autoTranslateCurrentLecture()          (only if opted in AND hits < total)
```

## Observability

Extends the `shared_*` table added in `eda9aca`:

| Side    | Event          | Fields                                                        |
|---------|----------------|---------------------------------------------------------------|
| local   | `lookup`       | `video_key`, `lines`, `hits`, `remote_hits`, `remote_status`   |
| central | `shared_lookup`| `remote`, `lines`, `cache_hits`, `status`, `duration_ms`        |

## Error handling

| Failure | Behavior |
|---|---|
| Daemon offline | Prefetch no-ops; overlay unaffected |
| Transcript extraction fails | Logged, no prefetch, no translation |
| Central 404 (old version) | `remote_status: unsupported`, local hits still returned |
| Central network failure | `remote_status: unavailable`, circuit opens, local hits still returned |
| Over 2000 lines | `400`; the extension does not chunk (transcripts are well under) |

## Testing

Daemon (Go, `:memory:` cache + mock provider):
- `/v1/lookup` returns cached lines and the mock provider is called **zero**
  times.
- Cross-provider hit: a row written as gemini is found when asking as codex.
- Remote merge: central has the line, local does not → returned, stored
  locally, `sync_outbox` stays empty.
- Central `404` → `remote_status: unsupported`, local hits intact, circuit
  still closed.
- Central shared `/v1/lookup` requires the token and calls no provider.

Extension (vitest + jsdom, added in this work):
- `prefetch` merge logic: hits patch the overlay and write to storage; empty
  result changes nothing.
- No translate request is issued by the prefetch path.

Manual: load a cached Udemy lecture with the daemon running and confirm
subtitles appear with no `translate_start` in `daemon.log`, then check for a
`lookup` event.

## Auto-translate after a partial hit

Today `autoTranslateCurrentLecture()` returns early when storage holds **any**
cached lines for the lecture. That was fine while storage was either empty or
the output of a complete translate job. Prefetch makes "the central had 60% of
the lines" a common state, and with the old check sticky auto-translate would
silently skip those lectures — prefetch would break the flow the user opted
into.

New rule: after prefetch, if the user has an `autoTranslateConfig` and
`hits < total`, run auto-translate for the lecture. Cost is bounded: the
orchestrator looks every line up in the cache before chunking, so only the
missing lines reach the provider. Without `autoTranslateConfig` nothing is
translated, same as today.

Implementation: `prefetchCachedTranslations()` returns `{hits, total}`;
`autoTranslateCurrentLecture()` takes that result instead of re-reading
storage and skips only when `hits === total`.

## Related work

The overlay click-to-reveal toggle (original-only by default, click to show
bilingual, persisted in `chrome.storage.local`) was implemented separately as a
bounded change. Default-hidden is what makes prefetch pleasant rather than
noisy: the translation is already there, waiting, one click away.
