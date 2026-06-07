import type { TranscriptPayload } from './transcript'
import type {
  ChunkDonePayload,
  ChunkErrorPayload,
  DonePayload,
  JobCreatedPayload,
  TranslateRequest,
} from './DaemonClient'

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
  | { type: 'CAPTION_SNAPSHOT' }

export type ExtractTranscriptResponse =
  | { ok: true; payload: TranscriptPayload }
  | { ok: false; code: string; message: string }

export type SimpleAck = { ok: true } | { ok: false; message: string }

export type DaemonStreamCommand =
  | { type: 'start'; request: TranslateRequest }
  | { type: 'cancel' }
  | { type: 'ping' }

export type DaemonStreamEvent =
  | { type: 'job-created'; payload: JobCreatedPayload }
  | { type: 'chunk-done'; payload: ChunkDonePayload }
  | { type: 'chunk-error'; payload: ChunkErrorPayload }
  | { type: 'done'; payload: DonePayload }
  | { type: 'fatal'; message: string }

export type OverlayStatus = {
  ok: true
  mounted: boolean
  liveMode: boolean
  videoKey: string | null
  translationsCount: number
}

// Diagnostic snapshot of caption-bearing DOM, returned by CAPTION_SNAPSHOT.
// This is debug-only: a broad probe to figure out WHERE the real caption is
// rendered when detection fails. It never feeds live caption selection.
export type CaptionCandidate = {
  // 'selector' = matched a known/broad caption-ish selector;
  // 'lower-video-leaf' = a text leaf sitting over the lower video area that
  // matched no selector — the case where detection is otherwise stuck.
  source: 'selector' | 'lower-video-leaf'
  tag: string
  id: string
  className: string
  role: string | null
  dataAttrs: Record<string, string>
  ariaAttrs: Record<string, string>
  rect: { x: number; y: number; w: number; h: number }
  overlapsVideo: boolean
  display: string
  visibility: string
  opacity: string
  childCount: number
  matchesAuthoritative: boolean
  text: string
}

export type CaptionSnapshot = {
  ok: true
  url: string
  site: string
  videoKey: string | null
  timestamp: number
  authoritativePresent: boolean
  authoritativeText: string | null
  video: { rect: { x: number; y: number; w: number; h: number }; readyState: number; textTracks: number } | null
  candidates: CaptionCandidate[]
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
