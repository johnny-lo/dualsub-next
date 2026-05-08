import type { ActiveCue, CueObserver } from './types'

/**
 * Wires a CueObserver to the page's video element via the HTML5 TextTrack API.
 * Works for any site whose player exposes textTracks (Netflix, Udemy, YouTube…).
 *
 * The returned disposer removes all listeners. If no <video> element is present
 * the observer is invoked once with null and a no-op disposer is returned.
 */
export function observeViaTextTracks(observer: CueObserver): () => void {
  const video = document.querySelector('video')
  if (!video) {
    observer(null)
    return () => {}
  }

  let lastTexts: string[] = []
  const sameAsLast = (next: string[]) =>
    next.length === lastTexts.length && next.every((t, i) => t === lastTexts[i])

  const handleCueChange = (track: TextTrack) => () => {
    const cues = track.activeCues
    if (!cues || cues.length === 0) {
      if (lastTexts.length > 0) {
        lastTexts = []
        observer(null)
      }
      return
    }
    // Keep each active cue separate — translation cache is keyed per cue.
    const texts: string[] = []
    let earliestStart = Infinity
    let latestEnd = 0
    for (const cue of Array.from(cues)) {
      const c = cue as VTTCue
      const t = c.text.replace(/<[^>]+>/g, '').trim()
      if (t) texts.push(t)
      earliestStart = Math.min(earliestStart, c.startTime)
      latestEnd = Math.max(latestEnd, c.endTime)
    }
    if (sameAsLast(texts)) return
    lastTexts = texts
    if (texts.length === 0) {
      observer(null)
    } else {
      observer({ texts, startTime: earliestStart, endTime: latestEnd } satisfies ActiveCue)
    }
  }

  const handlers: Array<{ track: TextTrack; fn: EventListener }> = []

  const attach = (track: TextTrack) => {
    if (track.mode === 'disabled') track.mode = 'hidden'
    const fn = handleCueChange(track) as EventListener
    track.addEventListener('cuechange', fn)
    handlers.push({ track, fn })
    // fire once for whatever is currently active
    fn(new Event('cuechange'))
  }

  for (const track of Array.from(video.textTracks)) attach(track)

  const onAddTrack = (e: Event) => {
    const evt = e as TrackEvent
    if (evt.track) attach(evt.track)
  }
  video.textTracks.addEventListener('addtrack', onAddTrack)

  return () => {
    for (const { track, fn } of handlers) track.removeEventListener('cuechange', fn)
    video.textTracks.removeEventListener('addtrack', onAddTrack)
  }
}
