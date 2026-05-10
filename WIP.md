# Work In Progress

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
