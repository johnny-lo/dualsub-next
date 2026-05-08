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

  let lastText = ''

  const handleCueChange = (track: TextTrack) => () => {
    const cues = track.activeCues
    if (!cues || cues.length === 0) {
      if (lastText !== '') {
        lastText = ''
        observer(null)
      }
      return
    }
    // Concatenate multi-line cues with a single space.
    const parts: string[] = []
    let earliestStart = Infinity
    let latestEnd = 0
    for (const cue of Array.from(cues)) {
      const c = cue as VTTCue
      parts.push(c.text.replace(/<[^>]+>/g, '').trim())
      earliestStart = Math.min(earliestStart, c.startTime)
      latestEnd = Math.max(latestEnd, c.endTime)
    }
    const text = parts.join(' ').trim()
    if (text === lastText) return
    lastText = text
    if (text === '') {
      observer(null)
    } else {
      observer({ text, startTime: earliestStart, endTime: latestEnd } satisfies ActiveCue)
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
