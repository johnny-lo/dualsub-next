import type { TranscriptEntry } from '@/shared/transcript'
import { observeViaTextTracks } from './cueObserver'
import { cuesToEntries, waitForCues } from './textTrack'
import { ExtractError, type CueObserver, type SubtitleExtractor } from './types'

function videoIdFromUrl(): string | null {
  const m = location.pathname.match(/\/watch\/(\d+)/)
  return m ? m[1] : null
}

export class NetflixExtractor implements SubtitleExtractor {
  readonly site = 'netflix' as const

  videoKey(): string {
    const id = videoIdFromUrl()
    return id ? `netflix:${id}` : `netflix:${location.pathname}`
  }

  title(): string {
    const el =
      document.querySelector('[data-uia="video-title"]') ??
      document.querySelector('h4.video-title')
    return el?.textContent?.trim() || document.title
  }

  async extractFullTranscript(preferredLang?: string): Promise<TranscriptEntry[]> {
    if (!videoIdFromUrl()) {
      throw new ExtractError('NOT_WATCH_PAGE', 'Open a Netflix /watch/<id> page first')
    }

    const video = document.querySelector('video')
    if (!video) {
      throw new ExtractError('NO_VIDEO_ELEMENT', 'No <video> element on the page')
    }

    if (video.textTracks.length === 0) {
      throw new ExtractError(
        'NO_TEXT_TRACKS',
        'Netflix has no textTracks loaded. Turn subtitles on in the Netflix player first.',
      )
    }

    const track = await waitForCues(video.textTracks, 8000, preferredLang)
    const entries = cuesToEntries(track)
    if (entries.length === 0) {
      throw new ExtractError('TEXTTRACK_EMPTY', 'TextTrack reported 0 cues')
    }
    console.log(`[DualSub] Netflix TextTrack → ${entries.length} cues`)
    return entries
  }

  observeCurrentCue(observer: CueObserver): () => void {
    return observeViaTextTracks(observer)
  }
}
