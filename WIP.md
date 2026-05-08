# Work In Progress

## Active issue: Udemy translation overlay missing Chinese

**Symptom:** On Udemy, popup reports translation succeeded (e.g. `55/55 lines`)
and progress bar reaches 100%. But on playback, the bilingual overlay either
(a) shows only the original English with no Chinese under it, or (b) does not
appear at all and the user only sees Udemy's own native subtitle. Netflix is
unaffected.

**Last known state (2026-05-08):**
- Lecture 46 ("Kubectl Apply Command", 英語 [自動]): overlay appeared with
  English but Chinese was empty.
- Lecture 45 ("Lab Solution - Imperative Commands", 英語 [CC]): our overlay
  did not appear at all; only Udemy's own subtitle was visible.

After the latest fix (DOM-based observation for Udemy), the user has not
retested yet. **Resuming this debug starts with the verification steps
below.**

## Diagnostic journey

### 1. cueObserver joined multi-cue with space — FIXED
`observeViaTextTracks` concatenated all active `VTTCue.text` values into a
single string. The translation cache stored each cue separately, so when
Udemy's player showed two simultaneous cue blocks (one sentence split across
two timestamps), the lookup key was `"cueA cueB"` but the cache had separate
`"cueA"` and `"cueB"` keys → lookup failed.

Changed `ActiveCue.text: string` → `ActiveCue.texts: string[]`;
cueObserver no longer joins; `SubtitleOverlay.render` now accepts
`string[] | null` and renders one `(original, translated)` pair per cue.

### 2. Tier 1 picks a different caption than the player shows — PARTIALLY ADDRESSED
`fetchVttUrl` picks the first `caption.locale_id.startsWith('en')`, which can
be `英語 [CC]` when the user is watching `英語 [自動]` (or vice versa).
Different caption tracks have different cue boundaries / text → cache stored
under track A's text → observation reads track B's text → no match.

First attempt: switched UdemyExtractor extraction to Tier 2 (textTracks)
first so extraction reads the same source as observation. This worked on
lecture 46 (where the [自動] track populated textTracks).

### 3. Udemy [CC] is not in browser textTracks — FIXED (pending verification)
On lecture 45 (英語 [CC]), `video.textTracks.length === 0` even with subtitles
enabled. Udemy renders [CC] caption text into a DOM element matching
`[class*="well--text"]` and never uses the HTML5 TextTrack API for it.

So Tier 2 always failed for [CC] → fell back to Tier 1 (REST API) → cache
populated → but cueObserver listened on textTracks (empty) → never fired →
overlay never rendered.

**Fix:** replaced `UdemyExtractor.observeCurrentCue` with
`observeUdemyCaptions` that polls `[class*="well--text"]` every 250ms.
Reverted extraction to Tier 1 first, since textTracks is unreliable on Udemy.

### 4. cueObserver disposer not reset on SPA lecture change — FIXED
Udemy switches between lectures via SPA without reload; the `<video>` element
is replaced. The previous cueObserver was still attached to the old element.
`startCueObserver` had `if (cueDisposer || ...) return`, so the second click
never re-attached. Removed the guard — re-attach always happens now.

## What is still uncertain

1. **Does Tier 1 REST API fetch the same caption that Udemy renders into
   `well--text`?** `fetchVttUrl` picks `captions.find(c =>
   c.locale_id?.startsWith('en'))` — that could be CC or 自動 depending on
   API ordering. If REST API returns [自動] but the player shows [CC]
   (or vice versa), the cache key still won't match the observed text. This
   is unverified for any lecture where multiple English captions exist.

2. **Is the new DOM polling responsive enough?** 250ms should be fine for
   subtitle pacing, but worth confirming on a fast-talking lecture.
   Alternative: switch to a `MutationObserver` on the video wrapper.

3. **Netflix path is untested** after the multi-cue refactor. Should still
   work — `NetflixExtractor` still uses `observeViaTextTracks` and
   `cuesToEntries`, both updated for the array shape.

## Verification protocol on resume

```powershell
# 1. Start the daemon (with auto-restart on config change)
.\dualsub-watch.ps1
```

1. Rebuild extension if any code changed: `cd extension; npm run build`
2. `chrome://extensions/` → DualSub Next → reload
3. F5 the Udemy lecture tab
4. Open DevTools Console
5. Click Translate. Confirm console prints
   `[DualSub] Udemy REST API → N cues`
6. Let the video play. Verify:
   - Our overlay (black bar with white English + yellow Chinese) appears.
   - English in our overlay matches Udemy's own subtitle **exactly**.
   - If English matches but Chinese is empty → uncertainty (1) above is
     the cause. The fix is to disambiguate captions in `fetchVttUrl` by
     reading the currently-selected entry from the captions menu DOM
     (`[role="menuitemradio"][aria-checked="true"]`) and matching its label
     against `caption.title` / `caption.locale_id`.

## Files most likely to need further changes

- `extension/src/content/extractors/UdemyExtractor.ts`
  - `fetchVttUrl`: caption disambiguation (uncertainty 1)
  - `observeUdemyCaptions`: poll cadence / MutationObserver upgrade
- `extension/src/content/extractors/NetflixExtractor.ts`
  - Spot-check after the `ActiveCue.texts: string[]` refactor
