# Work In Progress

## Session 2026-05-11: popup-safe translation + Udemy cue stability

### Issues fixed

#### 1. Popup close aborted full-transcript translation — FIXED
Chrome destroys the extension popup when the user clicks back into Udemy.
The full-transcript SSE stream used to live in the popup, so that teardown
aborted `/v1/translate` and left `/v1/jobs` looking stuck at `running`.

Fix:
- Popup now sends `START_TRANSLATE` to the content script.
- Content script owns the full-transcript daemon stream and patches the
  overlay/storage as chunks arrive.
- `CANCEL_TRANSLATE` still lets the popup stop the active full pass.
- Daemon cancellation now updates job status with `context.WithoutCancel`,
  so canceled jobs become `failed` or `partial` instead of staying `running`.

#### 2. Udemy cue fallback grabbed page UI text — FIXED
When a cue ended, the observer fell through to broad caption/subtitle DOM
selectors and could pick up labels like "English" or course-progress text.

Fix:
- Udemy live observation is limited to known caption containers.
- Empty cue detection waits 2 seconds before hiding the overlay. A new cue
  during that window cancels the pending hide and renders immediately.
- Non-cue labels such as `英文`, `English`, `字幕`, and `captions` are filtered.

#### 3. Dragged overlay anchored by left edge — FIXED
Dragging used the container's left/top point. If the next subtitle line had a
different width, the overlay appeared to grow from the left edge.

Fix:
- Dragging stores the pointer offset from the overlay center.
- Dragged positions use `translate(-50%, -50%)`, keeping the overlay anchored
  by its center point as text width changes.

### Files changed this session

- `daemon/internal/translate/orchestrator.go` — cancellation-safe job update
- `daemon/internal/translate/orchestrator_test.go` — regression test for
  canceled jobs not staying `running`
- `extension/src/content/extractors/UdemyExtractor.ts` — narrow cue selectors,
  non-cue label filtering, 2-second delayed hide
- `extension/src/content/overlay/SubtitleOverlay.ts` — center-point drag
  anchoring
- `extension/src/content/index.ts` — content-script-owned full translation
- `extension/src/popup/App.tsx` — starts/cancels background translation via
  content messages and polls recent jobs
- `extension/src/shared/messaging.ts` — `START_TRANSLATE` / `CANCEL_TRANSLATE`
- `README.md`, `codebase.md`, `WIP.md` — documentation updates

---

## Session 2026-05-10 (evening): cross-track lookup + sticky auto-translate

Linux end-to-end bring-up + 3 user-facing bugs + 1 small daemon gap noted.

### Environment

- Built daemon on Linux (`go1.22.2` against `go.mod` `go 1.25.0` — works
  but should be reconciled). All `go test ./...` green.
- Picked `gemini-flash-latest` after confirming `gemini-3-flash` is not a
  real API model name (404). Stable alias auto-tracks newest free-tier Flash.
- Ollama install deferred — sudo prompt couldn't be satisfied from the agent
  shell, and disk download too long for the session.

### Bugs fixed

#### 1. Overlay rendered English only, no Chinese — FIXED
Symptom: popup correctly reported "translated 23 lines" (via `PING_OVERLAY`
→ `overlay.translationsCount`) but every cue rendered with empty Chinese.

Root cause: **VTT-extracted text ≠ player-rendered DOM text**. Tier 1 REST
API parses the VTT file and uses VTT-source strings as map keys. The cue
observer reads `[data-purpose="captions-cue-text"]` from DOM. Smart quotes,
half/full-width punctuation, inline-tag stripping, line-break differences,
and multi-track ambiguity (`en-US` vs `en-auto-generated`) all mean the
keys never match.

Fixes (`shared/transcript.ts`, `content/overlay/SubtitleOverlay.ts`):
- `normalizeText` now does NFKC + lowercase + smart-quote/dash mapping +
  strips `\p{P}\p{S}` (preserves CJK chars via Unicode property escapes,
  not `\w` which would drop them).
- `lookupTranslation` now does (a) longest-substring containment with
  threshold dropped from 10/0.6 to 4/0.3, picking best-by-length not
  first-hit, (b) word-overlap Jaccard ≥ 0.6 fallback for word-order /
  punctuation drift.

#### 2. Native captions still visible despite hide CSS — FIXED
Hide CSS only matched `[class*="captions-display--"]` + `video::cue` —
missed `well--text--*` (legacy), `well--container--*`, `captions-cue--*`,
the cue-text element itself, and Netflix's `.player-timedtext`.

Fix (`SubtitleOverlay.hideNativeCaptions`): widened selector list, layered
`opacity:0` + `visibility:hidden` + `color:transparent` + `text-shadow:none`
for defense in depth. Updated `UdemyExtractor.isVisibleCaptionCandidate` to
bypass the style-based visibility filter when `#dualsub-hide-native-captions`
is mounted — otherwise our own hide style would defeat the fallback path
(re-introducing the WIP-1 issue from the morning session).

#### 3. Switching Udemy lecture left new lectures untranslated — FIXED
SPA-nav handler already detected pushState/replaceState/popstate and
called `stopAll()`, but only restored from cache on the new lecture. New
(uncached) lectures sat at "extracted English, no Chinese" until the user
manually clicked Translate again.

Fix (`content/index.ts` + `popup/App.tsx` + `shared/messaging.ts`):
- `SET_OVERLAY` message gained an `autoTranslate?: { provider, sourceLang,
  targetLang }` field. Popup includes it on every Translate.
- Content script stores it as `autoTranslateConfig`, persists to
  `chrome.storage.local` under `dualsubAutoTranslate`. Survives page
  reload + browser restart.
- `stopAll({ forNavigation: true })` preserves `liveMode` and
  `autoTranslateConfig` across SPA nav (only `CLEAR_OVERLAY` clears them).
- New `autoTranslateCurrentLecture()`: cache-checks first (no wasted daemon
  call), then extracts + streams translation through daemon, gating each
  chunk on `currentVideoKey === videoKey` to prevent late chunks from a
  prior lecture leaking into the new one.
- New `bootstrap()` runs at content-script load: restores `autoTranslateConfig`
  from storage, restores cached overlay, fires auto-translate if no cache
  and config present.

### Debug helper

`window.__dualsubDebug` exposed in content-script isolated world (also
`globalThis` mirror — CRXJS dynamic-import sometimes drops `window` writes).
Use from DevTools after switching context dropdown to "DualSub Next":

```js
__dualsubDebug.diag()      // cue text, sample stored keys, lookup result, sticky config
__dualsubDebug.dumpAll()   // full chrome.storage.local.dualsubTranslationCache
```

Install confirmation log: `[DualSub] __dualsubDebug installed: ["diag","dumpAll"]`.

### Known small gap (not fixed)

- `daemon/internal/translate/orchestrator.go:180` always passes `""` as the
  job summary on `cache.UpdateJob`, so `/v1/jobs` reports `error_summary: ""`
  even when chunks failed. SSE `chunk-error` events still carry the detail.

### Files changed this session

- `extension/src/content/extractors/UdemyExtractor.ts` — bypass visibility
  filter when hide style is mounted
- `extension/src/content/overlay/SubtitleOverlay.ts` — improved
  `lookupTranslation`, broader `hideNativeCaptions`, public
  `debugSampleKeys`
- `extension/src/content/index.ts` — `autoTranslateConfig` lifecycle,
  `autoTranslateCurrentLecture`, `bootstrap`, `__dualsubDebug` global,
  `forNavigation` flag on `stopAll`
- `extension/src/popup/App.tsx` — sends `autoTranslate` in `SET_OVERLAY`
- `extension/src/shared/messaging.ts` — `autoTranslate` field on
  `SET_OVERLAY` message
- `extension/src/shared/transcript.ts` — stronger `normalizeText`

---

## Session 2026-05-10: Udemy caption fix + overlay features

### Issues fixed

#### 1. Udemy caption selector picking wrong element — FIXED
`findUdemyCaptionText()` used broad CSS selectors (`[class*="caption"]`,
`[class*="subtitle"]`, etc.) and sorted candidates by `bottom` position.
This caused the course progress tooltip ("0/37 | 3小時 21分鐘已完成...")
to rank above the actual caption text (`bottom: 959` vs `bottom: 634`).

**Fix:**
- Added `[data-purpose="captions-cue-text"]` as the primary, exact selector.
- When this element exists, skip visibility check and return immediately
  (or return empty if no active cue — prevents fallback from grabbing
  unrelated text).
- Updated sort priority from `well--` to `captions-display--` (Udemy's
  current class prefix).
- Broad selectors only used as fallback when `captions-cue-text` element
  doesn't exist (legacy Udemy versions).

#### 2. Native captions hidden without breaking cue detection — FIXED
Overlay injects `<style>` to hide native Udemy captions using
`opacity: 0` + `pointer-events: none` (NOT `display: none`).
The `findUdemyCaptionText()` skips visibility check for the exact
`data-purpose="captions-cue-text"` match, so `textContent` is still
readable even when hidden.

On `overlay.destroy()`, the hide-style is removed to restore native captions.

#### 3. Overlay shows random text between cues — FIXED
When `captions-cue-text` exists but is empty (between subtitle cues),
the code fell through to fallback selectors, which matched unrelated
elements like "英語" or language menu items.

**Fix:** When `captions-cue-text` element exists, always use it as the
authoritative source. Return empty string (= hide overlay) instead of
falling through to broad selectors.

### Features added

#### 4. Subtitle overlay style settings — NEW
- `SubtitleOverlay` now accepts `OverlayStyle` (font size + color for both
  original and translated text).
- Styles stored in `chrome.storage.local` under key `dualsubOverlayStyle`.
- Overlay loads saved style on construction.
- Options page (`src/options/App.tsx`) has a new "Subtitle Overlay Style"
  section with `InputNumber` for sizes and `ColorPicker` for colors.

#### 5. Translation paste-back textarea — NEW
- Popup's "Fallback: copy original" section now has a textarea below the
  Extract & Copy button.
- User pastes translated text (in `[index] text` format) from ChatGPT/Gemini.
- "Apply to overlay" button parses the text using `parseTranslatedTranscript`,
  maps indices back to original text from the source payload, and sends
  `SET_OVERLAY` to the content script.

#### 6. Video translation status indicator — NEW
- Popup queries `PING_OVERLAY` on open and shows a status tag:
  - `translated (N lines)` — green tag when translations exist
  - `overlay active, no translations` — orange tag
  - `not translated` — default tag
  - `no subtitle page` — when content script isn't loaded
  - `Live` tag shown when live mode is active
- `SubtitleOverlay.translationsCount` getter exposed for accurate reporting.
- Status refreshes after translation completes or paste-back is applied.

## What is still uncertain

1. **Does Tier 1 REST API fetch the same caption that Udemy renders?**
   If REST API returns [自動] but the player shows [CC], cache keys won't
   match observed text. Unverified for lectures with multiple English captions.

2. **Netflix path untested** after recent changes. Should work — no Netflix
   code was modified.

## Verification protocol

```powershell
# 1. Start the daemon
.\dualsub-watch.ps1
```

1. Rebuild extension: `cd extension; npm run build`
2. `chrome://extensions/` → DualSub Next → reload
3. **F5 the Udemy lecture tab** (critical — content script won't load without page refresh)
4. Open DevTools Console
5. Click Translate. Confirm console prints `[DualSub] Udemy REST API → N cues`
6. Verify:
   - Native Udemy captions are hidden (opacity 0)
   - DualSub overlay shows white English + yellow Chinese
   - Between cues, overlay hides (no random text)
   - Popup shows "translated (N lines)" status

## Files changed this session

- `extension/src/content/extractors/UdemyExtractor.ts` — selector fix + cue detection
- `extension/src/content/overlay/SubtitleOverlay.ts` — hide native, style settings, translationsCount
- `extension/src/content/index.ts` — translationsCount in PING_OVERLAY
- `extension/src/popup/App.tsx` — paste-back textarea, overlay status indicator
- `extension/src/options/App.tsx` — subtitle style settings UI
