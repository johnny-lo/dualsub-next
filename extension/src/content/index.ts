import { detectSite } from './extractors'
import { ExtractError, type ActiveCue } from './extractors/types'
import { SubtitleOverlay } from './overlay/SubtitleOverlay'
import { DaemonClient } from '@/shared/DaemonClient'
import type {
  ContentMessage,
  ExtractTranscriptResponse,
  OverlayStatus,
  SimpleAck,
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

function stopAll() {
  cueDisposer?.()
  cueDisposer = null
  overlay?.destroy()
  overlay = null
  currentVideoKey = null
  liveMode = false
  inflightLive.clear()
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

    if (msg.type === 'PING_OVERLAY') {
      sendResponse({
        ok: true,
        mounted: overlay !== null,
        liveMode,
        videoKey: currentVideoKey,
        translationsCount: overlay?.translationsCount ?? 0,
      } satisfies OverlayStatus)
      return false
    }

    return false
  },
)

// On load: if this video already has translations cached from a prior
// session, mount the overlay immediately so playback shows Chinese without
// the user having to click Translate again.
void restoreOverlayFromStorage()

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
    stopAll()
    // Wait a tick for Udemy to mount the new lecture's DOM before we
    // try to query videoKey / well--text.
    setTimeout(() => {
      void restoreOverlayFromStorage()
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
