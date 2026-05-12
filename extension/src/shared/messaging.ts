import type { TranscriptPayload } from './transcript'
import type { TranslateRequest } from './DaemonClient'

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
  | { type: 'HIDE_OVERLAY' }
  | { type: 'SHOW_OVERLAY' }
  | { type: 'SET_LIVE_MODE'; enabled: boolean; provider?: string; targetLang?: string }
  | {
      type: 'START_TRANSLATE'
      payload: TranscriptPayload
      request: TranslateRequest
    }
  | { type: 'CANCEL_TRANSLATE' }
  | { type: 'GET_TRANSLATE_STATUS' }
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

export type TranslateStatus =
  | {
      ok: true
      active: false
      status: 'idle'
    }
  | {
      ok: true
      active: true
      jobId: string | null
      videoKey: string
      provider: string
      status: 'running' | 'completed' | 'partial' | 'failed' | 'aborted'
      totalChunks: number
      completedChunks: number
      failedChunks: number
      totalLines: number
      translatedLines: number
      cacheHits: number
      startedAt: number
      updatedAt: number
      errorSummary?: string
    }

export async function sendToActiveTab<R>(message: ContentMessage): Promise<R> {
  const [tab] = await chrome.tabs.query({ active: true, currentWindow: true })
  if (!tab?.id) throw new Error('No active tab')
  return chrome.tabs.sendMessage<ContentMessage, R>(tab.id, message)
}
