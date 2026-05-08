import type { TranscriptEntry } from '@/shared/transcript'

export type SiteId = 'netflix' | 'udemy'

export type CueObserver = (cue: ActiveCue | null) => void

export interface ActiveCue {
  text: string
  startTime: number
  endTime: number
}

export interface SubtitleExtractor {
  readonly site: SiteId
  videoKey(): string
  title(): string
  extractFullTranscript(preferredLang?: string): Promise<TranscriptEntry[]>
  /**
   * Subscribe to the player's current cue. The observer is called with the active
   * cue text whenever it changes, and with null when no cue is currently active.
   * Returns a disposer that removes the listener.
   */
  observeCurrentCue(observer: CueObserver): () => void
}

export class ExtractError extends Error {
  constructor(
    public code: string,
    message: string,
  ) {
    super(message)
    this.name = 'ExtractError'
  }
}
