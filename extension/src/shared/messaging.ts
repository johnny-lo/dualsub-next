import type { TranscriptPayload } from './transcript'

export type ContentMessage =
  | { type: 'EXTRACT_TRANSCRIPT'; preferredLang?: string }
  | {
      type: 'SET_OVERLAY'
      videoKey: string
      translations: Record<string, string> // replaces existing map
      // When present, content script remembers this config and will
      // auto-translate subsequent lectures (Udemy SPA navigation) that
      // haven't been cached yet — until CLEAR_OVERLAY explicitly opts out.
      autoTranslate?: {
        provider: string
        sourceLang: string
        targetLang: string
      }
    }
  | {
      type: 'PATCH_OVERLAY'
      videoKey: string
      translations: Record<string, string> // merges into existing map
    }
  | { type: 'CLEAR_OVERLAY' }
  | { type: 'SET_LIVE_MODE'; enabled: boolean; provider?: string; targetLang?: string }
  | { type: 'PING_OVERLAY' }

export type ExtractTranscriptResponse =
  | { ok: true; payload: TranscriptPayload }
  | { ok: false; code: string; message: string }

export type SimpleAck = { ok: true } | { ok: false; message: string }

export type OverlayStatus = {
  ok: true
  mounted: boolean
  liveMode: boolean
  videoKey: string | null
  translationsCount: number
}

export async function sendToActiveTab<R>(message: ContentMessage): Promise<R> {
  const [tab] = await chrome.tabs.query({ active: true, currentWindow: true })
  if (!tab?.id) throw new Error('No active tab')
  return chrome.tabs.sendMessage<ContentMessage, R>(tab.id, message)
}
