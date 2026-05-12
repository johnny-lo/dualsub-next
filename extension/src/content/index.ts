import { detectSite } from './extractors'
import { ExtractError, type ActiveCue } from './extractors/types'
import { SubtitleOverlay } from './overlay/SubtitleOverlay'
import { DaemonClient } from '@/shared/DaemonClient'
import { normalizeText } from '@/shared/transcript'
import type {
  ContentMessage,
  ExtractTranscriptResponse,
  OverlayStatus,
  SimpleAck,
  TranslateStatus,
} from '@/shared/messaging'

const extractor = detectSite()

console.log(
  `[DualSub] content script loaded on ${location.hostname}, site=${extractor?.site ?? 'unsupported'}`,
)

let overlay: SubtitleOverlay | null = null
let cueDisposer: (() => void) | null = null
let currentVideoKey: string | null = null
let liveMode = false
let liveProvider = ''
let liveTargetLang = '繁體中文'
const inflightLive = new Set<string>()
const daemon = new DaemonClient()

// "Sticky" full-pass translate config — set when the user clicks Translate
// (popup sends SET_OVERLAY with autoTranslate). Persists across SPA lecture
// navigation so each new lecture is auto-translated with the same provider
// without the user having to click again. Cleared by CLEAR_OVERLAY.
const AUTO_TRANSLATE_KEY = 'dualsubAutoTranslate'
interface AutoTranslateConfig {
  provider: string
  sourceLang: string
  targetLang: string
}
let autoTranslateConfig: AutoTranslateConfig | null = null
let fullTranslateAbort: (() => void) | null = null
let fullTranslateStatus: TranslateStatus = { ok: true, active: false, status: 'idle' }

// ─── persistent overlay cache ───────────────────────────────────────────
// Translations are stored per videoKey in chrome.storage.local so that
// page reloads and SPA lecture switches don't force the user to re-translate.
const STORAGE_KEY = 'dualsubTranslationCache'

interface StoredEntry {
  translations: Record<string, string>
  updatedAt: number
}
type StoredCache = Record<string, StoredEntry>

// Serialize storage writes — multiple PATCH_OVERLAY messages can race a
// read-modify-write cycle and clobber each other otherwise.
let writeChain: Promise<void> = Promise.resolve()

function readStoredTranslations(videoKey: string): Promise<Record<string, string> | null> {
  return new Promise((resolve) => {
    chrome.storage.local.get([STORAGE_KEY], (data) => {
      const cache = (data[STORAGE_KEY] ?? {}) as StoredCache
      const entry = cache[videoKey]
      resolve(entry && Object.keys(entry.translations).length > 0 ? entry.translations : null)
    })
  })
}

function writeStoredTranslations(
  videoKey: string,
  translations: Record<string, string>,
  mode: 'replace' | 'merge',
): Promise<void> {
  const op = () =>
    new Promise<void>((resolve) => {
      chrome.storage.local.get([STORAGE_KEY], (data) => {
        const cache = (data[STORAGE_KEY] ?? {}) as StoredCache
        const existing = cache[videoKey]?.translations ?? {}
        const merged = mode === 'replace' ? translations : { ...existing, ...translations }
        cache[videoKey] = { translations: merged, updatedAt: Date.now() }
        chrome.storage.local.set({ [STORAGE_KEY]: cache }, () => resolve())
      })
    })
  writeChain = writeChain.then(op, op)
  return writeChain
}

async function restoreOverlayFromStorage(): Promise<void> {
  if (!extractor) return
  const videoKey = extractor.videoKey()
  const stored = await readStoredTranslations(videoKey)
  if (!stored) return
  console.log(
    `[DualSub] restoring ${Object.keys(stored).length} cached translations for ${videoKey}`,
  )
  currentVideoKey = videoKey
  ensureOverlay().setTranslations(stored)
  startCueObserver()
}

function ensureOverlay(): SubtitleOverlay {
  if (!overlay) overlay = new SubtitleOverlay()
  return overlay
}

function startCueObserver() {
  if (!extractor) return
  // Always re-attach. The video element gets swapped during Udemy SPA
  // lecture-to-lecture navigation, so a previous disposer would be stale.
  cueDisposer?.()
  cueDisposer = extractor.observeCurrentCue(handleCue)
}

// `forNavigation`: tear down DOM-bound state (overlay, cue observer) but keep
// liveMode + autoTranslateConfig so the new lecture inherits user intent.
// Default (no flag) wipes everything — used by CLEAR_OVERLAY (explicit opt-out).
function stopAll(opts?: { forNavigation?: boolean }) {
  cueDisposer?.()
  cueDisposer = null
  overlay?.destroy()
  overlay = null
  currentVideoKey = null
  inflightLive.clear()
  fullTranslateAbort?.()
  fullTranslateAbort = null
  if (fullTranslateStatus.active && fullTranslateStatus.status === 'running') {
    fullTranslateStatus = {
      ...fullTranslateStatus,
      status: 'aborted',
      updatedAt: Date.now(),
    }
  }
  if (!opts?.forNavigation) {
    liveMode = false
    autoTranslateConfig = null
    void chrome.storage.local.remove(AUTO_TRANSLATE_KEY)
  }
}

function setFullTranslateStatus(patch: Partial<Extract<TranslateStatus, { active: true }>>) {
  if (!fullTranslateStatus.active) return
  fullTranslateStatus = {
    ...fullTranslateStatus,
    ...patch,
    updatedAt: Date.now(),
  }
}

function isCurrentFullTranslate(videoKey: string): boolean {
  return fullTranslateStatus.active && fullTranslateStatus.videoKey === videoKey
}

function hideOverlayOnly() {
  cueDisposer?.()
  cueDisposer = null
  overlay?.destroy()
  overlay = null
  currentVideoKey = null
  inflightLive.clear()
}

async function showOverlayForCurrentVideo(): Promise<SimpleAck> {
  if (!extractor) return { ok: false, message: 'unsupported site' }
  const videoKey = extractor.videoKey()
  const stored = await readStoredTranslations(videoKey)
  currentVideoKey = videoKey
  ensureOverlay().setTranslations(stored ?? {})
  startCueObserver()
  return { ok: true }
}

function startFullTranscriptTranslate(
  videoKey: string,
  entries: Array<{ index: number; originalText: string }>,
  request: Parameters<DaemonClient['translate']>[0],
) {
  currentVideoKey = videoKey
  ensureOverlay().setTranslations({})
  startCueObserver()
  fullTranslateStatus = {
    ok: true,
    active: true,
    jobId: null,
    videoKey,
    provider: request.provider,
    status: 'running',
    totalChunks: 0,
    completedChunks: 0,
    failedChunks: 0,
    totalLines: entries.length,
    translatedLines: 0,
    cacheHits: 0,
    startedAt: Date.now(),
    updatedAt: Date.now(),
  }

  const byIndex = new Map<number, string>()
  for (const e of entries) byIndex.set(e.index, e.originalText)

  fullTranslateAbort?.()
  fullTranslateAbort = daemon.translate(request, {
    onJobCreated: (info) => {
      if (!isCurrentFullTranslate(videoKey)) return
      setFullTranslateStatus({
        jobId: info.job_id,
        totalChunks: info.total_chunks,
        totalLines: info.total_lines,
        cacheHits: info.cache_hits,
      })
    },
    onChunkDone: (ck) => {
      if (!isCurrentFullTranslate(videoKey)) return
      const map: Record<string, string> = {}
      for (const l of ck.lines) {
        const orig = byIndex.get(l.index)
        if (orig) map[orig] = l.text
      }
      if (Object.keys(map).length > 0) {
        if (overlay && currentVideoKey === videoKey) overlay.patchTranslations(map)
        void writeStoredTranslations(videoKey, map, 'merge')
      }
      setFullTranslateStatus({
        completedChunks:
          ck.source === 'llm' && fullTranslateStatus.active
            ? fullTranslateStatus.completedChunks + 1
            : fullTranslateStatus.active
              ? fullTranslateStatus.completedChunks
              : 0,
        translatedLines: fullTranslateStatus.active
          ? fullTranslateStatus.translatedLines + ck.lines.length
          : ck.lines.length,
      })
    },
    onChunkError: (err) => {
      if (!isCurrentFullTranslate(videoKey)) return
      if (!err.final) return
      setFullTranslateStatus({
        failedChunks: fullTranslateStatus.active
          ? fullTranslateStatus.failedChunks + 1
          : 1,
        errorSummary: `${err.code}: ${err.message}`,
      })
    },
    onDone: (done) => {
      if (!isCurrentFullTranslate(videoKey)) return
      if (currentVideoKey === videoKey) fullTranslateAbort = null
      setFullTranslateStatus({
        status: done.failed > 0 && done.completed > 0
          ? 'partial'
          : done.failed > 0
            ? 'failed'
            : 'completed',
        completedChunks: done.completed,
        failedChunks: done.failed,
        cacheHits: done.cache_hits,
      })
      console.log(`[DualSub] full translate done for ${videoKey}`)
    },
    onFatal: (err) => {
      if (!isCurrentFullTranslate(videoKey)) return
      if (currentVideoKey === videoKey) fullTranslateAbort = null
      setFullTranslateStatus({
        status: 'failed',
        errorSummary: err.message,
      })
      console.warn(`[DualSub] full translate failed for ${videoKey}:`, err.message)
    },
  })
}

async function autoTranslateCurrentLecture(): Promise<void> {
  if (!extractor || extractor.site !== 'udemy' || !autoTranslateConfig) return
  const videoKey = extractor.videoKey()
  if (isCurrentFullTranslate(videoKey) && fullTranslateStatus.status === 'running') return

  // Cache hit → restoreOverlayFromStorage already handled it; skip the daemon call.
  const cached = await readStoredTranslations(videoKey)
  if (cached) return

  fullTranslateStatus = {
    ok: true,
    active: true,
    jobId: null,
    videoKey,
    provider: autoTranslateConfig.provider,
    status: 'running',
    totalChunks: 0,
    completedChunks: 0,
    failedChunks: 0,
    totalLines: 0,
    translatedLines: 0,
    cacheHits: 0,
    startedAt: Date.now(),
    updatedAt: Date.now(),
  }

  let entries
  try {
    entries = await extractor.extractFullTranscript(autoTranslateConfig.sourceLang)
  } catch (err) {
    setFullTranslateStatus({
      status: 'failed',
      errorSummary: err instanceof Error ? err.message : String(err),
    })
    console.warn('[DualSub] auto-translate: extract failed:', err)
    return
  }
  if (entries.length === 0) return
  if (!isCurrentFullTranslate(videoKey) || fullTranslateStatus.status !== 'running') return

  console.log(`[DualSub] auto-translating ${entries.length} cues for ${videoKey}`)
  startFullTranscriptTranslate(videoKey, entries, {
    site: extractor.site,
    video_key: videoKey,
    title: extractor.title(),
    provider: autoTranslateConfig.provider,
    source_lang: autoTranslateConfig.sourceLang,
    target_lang: autoTranslateConfig.targetLang,
    lines: entries.map((e) => ({ index: e.index, text: e.originalText })),
  })
}

function handleCue(cue: ActiveCue | null) {
  if (!overlay) return
  if (!cue) {
    overlay.render(null)
    return
  }
  overlay.render(cue.texts)
  if (!liveMode || !liveProvider || !extractor) return
  for (const text of cue.texts) {
    if (overlay.hasTranslation(text)) continue
    if (inflightLive.has(text)) continue
    inflightLive.add(text)
    const captured = text
    const currentTexts = cue.texts
    daemon.translate(
      {
        site: extractor.site,
        video_key: extractor.videoKey(),
        title: extractor.title(),
        provider: liveProvider,
        source_lang: 'auto',
        target_lang: liveTargetLang,
        lines: [{ index: 1, text: captured }],
      },
      {
        onChunkDone: (ck) => {
          if (ck.lines.length > 0 && overlay) {
            overlay.patchTranslations({ [captured]: ck.lines[0].text })
            // re-render the same set of cues so the new translation shows up
            overlay.render(currentTexts)
          }
        },
        onDone: () => inflightLive.delete(captured),
        onFatal: () => inflightLive.delete(captured),
      },
    )
  }
}

chrome.runtime.onMessage.addListener(
  (msg: ContentMessage, _sender, sendResponse: (r: unknown) => void) => {
    if (msg.type === 'EXTRACT_TRANSCRIPT') {
      if (!extractor) {
        sendResponse({
          ok: false,
          code: 'UNSUPPORTED_SITE',
          message: `${location.hostname} is not a supported site`,
        } satisfies ExtractTranscriptResponse)
        return false
      }
      extractor
        .extractFullTranscript(msg.preferredLang)
        .then((entries) => {
          sendResponse({
            ok: true,
            payload: {
              site: extractor.site,
              videoKey: extractor.videoKey(),
              title: extractor.title(),
              sourceLang: msg.preferredLang,
              entries,
            },
          } satisfies ExtractTranscriptResponse)
        })
        .catch((err) => {
          if (err instanceof ExtractError) {
            sendResponse({ ok: false, code: err.code, message: err.message })
          } else {
            sendResponse({ ok: false, code: 'UNEXPECTED', message: (err as Error).message })
          }
        })
      return true
    }

    if (msg.type === 'SET_OVERLAY') {
      if (!extractor) {
        sendResponse({ ok: false, message: 'unsupported site' } satisfies SimpleAck)
        return false
      }
      currentVideoKey = msg.videoKey
      ensureOverlay().setTranslations(msg.translations)
      startCueObserver()
      void writeStoredTranslations(msg.videoKey, msg.translations, 'replace')
      if (msg.autoTranslate) {
        autoTranslateConfig = msg.autoTranslate
        void chrome.storage.local.set({ [AUTO_TRANSLATE_KEY]: msg.autoTranslate })
      }
      sendResponse({ ok: true } satisfies SimpleAck)
      return false
    }

    if (msg.type === 'PATCH_OVERLAY') {
      if (!overlay || currentVideoKey !== msg.videoKey) {
        sendResponse({ ok: false, message: 'overlay not active for this video' } satisfies SimpleAck)
        return false
      }
      overlay.patchTranslations(msg.translations)
      void writeStoredTranslations(msg.videoKey, msg.translations, 'merge')
      sendResponse({ ok: true } satisfies SimpleAck)
      return false
    }

    if (msg.type === 'CLEAR_OVERLAY') {
      stopAll()
      sendResponse({ ok: true } satisfies SimpleAck)
      return false
    }

    if (msg.type === 'HIDE_OVERLAY') {
      hideOverlayOnly()
      sendResponse({ ok: true } satisfies SimpleAck)
      return false
    }

    if (msg.type === 'SHOW_OVERLAY') {
      showOverlayForCurrentVideo().then(sendResponse)
      return true
    }

    if (msg.type === 'SET_LIVE_MODE') {
      if (!extractor) {
        sendResponse({ ok: false, message: 'unsupported site' } satisfies SimpleAck)
        return false
      }
      liveMode = msg.enabled
      if (msg.provider) liveProvider = msg.provider
      if (msg.targetLang) liveTargetLang = msg.targetLang
      if (msg.enabled) {
        ensureOverlay()
        startCueObserver()
      }
      sendResponse({ ok: true } satisfies SimpleAck)
      return false
    }

    if (msg.type === 'START_TRANSLATE') {
      startFullTranscriptTranslate(msg.payload.videoKey, msg.payload.entries, msg.request)
      sendResponse({ ok: true } satisfies SimpleAck)
      return false
    }

    if (msg.type === 'CANCEL_TRANSLATE') {
      fullTranslateAbort?.()
      fullTranslateAbort = null
      setFullTranslateStatus({ status: 'aborted' })
      sendResponse({ ok: true } satisfies SimpleAck)
      return false
    }

    if (msg.type === 'GET_TRANSLATE_STATUS') {
      sendResponse(fullTranslateStatus)
      return false
    }

    if (msg.type === 'PING_OVERLAY') {
      sendResponse({
        ok: true,
        mounted: overlay !== null,
        liveMode,
        videoKey: currentVideoKey ?? extractor?.videoKey() ?? null,
        translationsCount: overlay?.translationsCount ?? 0,
      } satisfies OverlayStatus)
      return false
    }

    return false
  },
)

// Debug helper — accessible in DevTools after switching the Console context
// dropdown from "top" to the DualSub Next content-script context. Use:
//   __dualsubDebug.diag()      → cue text, sample keys, lookup result
//   __dualsubDebug.dumpAll()   → full translation map (large)
//
// Assign to BOTH window and globalThis: in CRXJS-loaded content scripts the
// isolated-world `window` binding is sometimes a Proxy that drops dynamically
// added properties; globalThis is a direct ref to the real global object.
const debugApi = {
  diag() {
    const cueText =
      document
        .querySelector<HTMLElement>('[data-purpose="captions-cue-text"]')
        ?.textContent?.trim() ?? '(no captions-cue-text element)'
    return {
      site: extractor?.site ?? 'unsupported',
      videoKey: extractor?.videoKey() ?? null,
      currentVideoKey,
      hasOverlay: overlay !== null,
      translationCount: overlay?.translationsCount ?? 0,
      sampleStoredKeys: overlay?.debugSampleKeys(8) ?? [],
      currentCueText: cueText,
      currentCueNormalized: normalizeText(cueText),
      lookupSucceeds: overlay?.hasTranslation(cueText) ?? false,
      liveMode,
      autoTranslateConfig,
    }
  },
  async dumpAll() {
    return new Promise<unknown>((resolve) => {
      chrome.storage.local.get(['dualsubTranslationCache'], (data) => resolve(data))
    })
  },
}
;(globalThis as unknown as { __dualsubDebug: typeof debugApi }).__dualsubDebug = debugApi
;(window as unknown as { __dualsubDebug: typeof debugApi }).__dualsubDebug = debugApi
console.log('[DualSub] __dualsubDebug installed:', Object.keys(debugApi))

// On load: restore the sticky auto-translate config first (so SPA nav after
// a content-script restart still inherits user intent), then mount any
// cached overlay. If no cache but autoTranslate is set for this fresh page,
// kick off an auto-translate so the user doesn't have to click Translate
// after a full reload.
async function bootstrap() {
  await new Promise<void>((resolve) => {
    chrome.storage.local.get([AUTO_TRANSLATE_KEY], (data) => {
      const cfg = data[AUTO_TRANSLATE_KEY] as AutoTranslateConfig | undefined
      if (cfg && typeof cfg.provider === 'string') autoTranslateConfig = cfg
      resolve()
    })
  })
  await restoreOverlayFromStorage()
  if (!overlay && autoTranslateConfig) {
    await autoTranslateCurrentLecture()
  }
}
void bootstrap()

// Udemy switches between lectures via SPA navigation — the URL changes via
// pushState without a page reload, the <video> element is replaced, and the
// overlay/translations from the previous lecture become stale. Detect the
// change, tear down the old overlay, and try to restore from storage for the
// new videoKey.
if (extractor?.site === 'udemy') {
  let lastPathname = location.pathname
  const handleNavigation = () => {
    if (location.pathname === lastPathname) return
    lastPathname = location.pathname
    console.log('[DualSub] Udemy SPA navigation, refreshing overlay')
    stopAll({ forNavigation: true })
    // Wait a tick for Udemy to mount the new lecture's DOM before we
    // try to query videoKey / well--text.
    setTimeout(async () => {
      await restoreOverlayFromStorage()
      // If the new lecture wasn't cached but the user previously opted in to
      // translation (autoTranslateConfig present), auto-fire a fresh
      // translate so they don't have to click again on every lecture.
      if (!overlay && autoTranslateConfig) {
        await autoTranslateCurrentLecture()
      }
      // Re-arm live mode for the new lecture if it was on previously.
      if (liveMode) {
        ensureOverlay()
        startCueObserver()
      }
    }, 500)
  }
  const origPushState = history.pushState.bind(history)
  history.pushState = (...args: Parameters<typeof history.pushState>) => {
    origPushState(...args)
    handleNavigation()
  }
  const origReplaceState = history.replaceState.bind(history)
  history.replaceState = (...args: Parameters<typeof history.replaceState>) => {
    origReplaceState(...args)
    handleNavigation()
  }
  window.addEventListener('popstate', handleNavigation)
}
