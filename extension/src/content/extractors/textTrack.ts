import type { TranscriptEntry } from '@/shared/transcript'
import { ExtractError } from './types'

export async function waitForCues(
  textTracks: TextTrackList,
  timeoutMs: number,
  preferredLang?: string,
): Promise<TextTrack> {
  const pickReady = () => {
    const tracks = Array.from(textTracks).filter((t) => t.cues && t.cues.length > 0)
    if (tracks.length === 0) return null
    if (preferredLang) {
      const match = tracks.find((t) => t.language?.startsWith(preferredLang))
      if (match) return match
    }
    return tracks[0]
  }

  const ready = pickReady()
  if (ready) return ready

  return new Promise<TextTrack>((resolve, reject) => {
    const timer = setTimeout(() => {
      reject(new ExtractError('TEXTTRACK_TIMEOUT', 'TextTrack cues did not load in time'))
    }, timeoutMs)

    const tryResolve = () => {
      const track = pickReady()
      if (track) {
        clearTimeout(timer)
        resolve(track)
      }
    }

    for (const track of Array.from(textTracks)) {
      if (track.mode === 'disabled') track.mode = 'hidden'
      track.addEventListener('cuechange', tryResolve)
    }

    textTracks.addEventListener('addtrack', (e: TrackEvent) => {
      const newTrack = e.track as TextTrack
      if (newTrack.mode === 'disabled') newTrack.mode = 'hidden'
      newTrack.addEventListener('cuechange', tryResolve)
      tryResolve()
    })
  })
}

export function cuesToEntries(track: TextTrack): TranscriptEntry[] {
  if (!track.cues) return []
  return Array.from(track.cues).map((cue, i) => {
    const c = cue as VTTCue
    return {
      index: i + 1,
      originalText: c.text.replace(/<[^>]+>/g, '').trim(),
      startTime: c.startTime,
      endTime: c.endTime,
    }
  })
}
