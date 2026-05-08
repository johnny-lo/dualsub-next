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

function ensureOverlay(): SubtitleOverlay {
  if (!overlay) overlay = new SubtitleOverlay()
  return overlay
}

function startCueObserver() {
  if (cueDisposer || !extractor) return
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
  overlay.render(cue.text)
  if (liveMode && liveProvider && extractor && !overlay.hasTranslation(cue.text)) {
    const text = cue.text
    if (inflightLive.has(text)) return
    inflightLive.add(text)
    daemon.translate(
      {
        site: extractor.site,
        video_key: extractor.videoKey(),
        title: extractor.title(),
        provider: liveProvider,
        source_lang: 'auto',
        target_lang: liveTargetLang,
        lines: [{ index: 1, text }],
      },
      {
        onChunkDone: (ck) => {
          if (ck.lines.length > 0 && overlay) {
            overlay.patchTranslations({ [text]: ck.lines[0].text })
            // re-render if still active
            overlay.render(text)
          }
        },
        onDone: () => inflightLive.delete(text),
        onFatal: () => inflightLive.delete(text),
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
      sendResponse({ ok: true } satisfies SimpleAck)
      return false
    }

    if (msg.type === 'PATCH_OVERLAY') {
      if (!overlay || currentVideoKey !== msg.videoKey) {
        sendResponse({ ok: false, message: 'overlay not active for this video' } satisfies SimpleAck)
        return false
      }
      overlay.patchTranslations(msg.translations)
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
        translationsCount: 0, // SubtitleOverlay doesn't expose this; fine for status check
      } satisfies OverlayStatus)
      return false
    }

    return false
  },
)
