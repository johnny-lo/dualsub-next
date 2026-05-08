import type { TranscriptEntry } from '@/shared/transcript'
import { parseVtt } from './parseVtt'
import { cuesToEntries, waitForCues } from './textTrack'
import { ExtractError, type ActiveCue, type CueObserver, type SubtitleExtractor } from './types'

// Udemy renders the active caption text into a DOM element they own
// (`well--text--<hash>`) instead of relying on the browser's TextTrack API,
// at least for [CC] tracks. We combine a MutationObserver (sub-frame
// reactivity when Udemy swaps the cue text) with a 300ms interval (catches
// cases where the observer doesn't fire — e.g. textContent changes inside
// nodes that are themselves replaced before MO sees them).
const UDEMY_CUE_SELECTOR = '[class*="well--text"]'

function observeUdemyCaptions(observer: CueObserver): () => void {
  let lastText = ''
  const emit = (text: string) => {
    if (text === lastText) return
    lastText = text
    if (text === '') {
      observer(null)
    } else {
      observer({ texts: [text], startTime: 0, endTime: 0 } satisfies ActiveCue)
    }
  }
  const tick = () => {
    const el = document.querySelector<HTMLElement>(UDEMY_CUE_SELECTOR)
    emit(el?.textContent?.trim() ?? '')
  }
  const interval = window.setInterval(tick, 300)
  const mo = new MutationObserver(tick)
  mo.observe(document.body, { childList: true, subtree: true, characterData: true })
  tick()
  return () => {
    window.clearInterval(interval)
    mo.disconnect()
  }
}

const UDEMY_HEADERS: Record<string, string> = {
  'X-Requested-With': 'XMLHttpRequest',
  Accept: 'application/json, text/plain, */*',
}

interface ParsedUrl {
  slug: string
  lectureId: string
}

function parseUrl(): ParsedUrl | null {
  const m = location.pathname.match(/\/course\/([^/]+)\/learn\/lecture\/(\d+)/)
  return m ? { slug: m[1], lectureId: m[2] } : null
}

function getCourseIdFromPage(): string | null {
  const moduleEl = document.querySelector('[data-module-args]')
  if (moduleEl) {
    try {
      const args = JSON.parse(moduleEl.getAttribute('data-module-args')!)
      if (args.courseId) return String(args.courseId)
    } catch {
      // fall through
    }
  }

  const clpEl = document.querySelector('[data-clp-course-id]')
  if (clpEl) return clpEl.getAttribute('data-clp-course-id')

  const bodyHtml = document.body.innerHTML
  const m1 = bodyHtml.match(/"courseId"\s*:\s*(\d+)/)
  if (m1) return m1[1]

  for (const script of Array.from(document.querySelectorAll('script:not([src])'))) {
    const text = script.textContent ?? ''
    const m = text.match(/"courseId"\s*:\s*(\d+)/) ?? text.match(/"course_id"\s*:\s*(\d+)/)
    if (m) return m[1]
  }
  return null
}

async function fetchCourseId(slug: string): Promise<string> {
  const fromPage = getCourseIdFromPage()
  if (fromPage) return fromPage

  const url = `https://www.udemy.com/api-2.0/courses/${slug}/?fields[course]=id`
  const res = await fetch(url, { credentials: 'include', headers: UDEMY_HEADERS })
  if (!res.ok) {
    throw new ExtractError('UDEMY_COURSE_ID_FAILED', `courseId fetch failed: ${res.status}`)
  }
  const data = await res.json()
  if (!data?.id) {
    throw new ExtractError('UDEMY_COURSE_ID_FAILED', 'courseId missing in response')
  }
  return String(data.id)
}

function findVttUrlFromPage(): string | null {
  const matchVtt = (s: string) => s.match(/https:\/\/[^"'\s]+\.vtt(?:\?[^"'\s]*)?"/)
  const m = matchVtt(document.body.innerHTML)
  if (m) return m[0].replace(/"$/, '')

  for (const script of Array.from(document.querySelectorAll('script:not([src])'))) {
    const m2 = matchVtt(script.textContent ?? '')
    if (m2) return m2[0].replace(/"$/, '')
  }
  return null
}

async function fetchVttUrl(courseId: string, lectureId: string, lang: string): Promise<string> {
  const fromPage = findVttUrlFromPage()
  if (fromPage) return fromPage

  const endpoints = [
    `https://www.udemy.com/api-2.0/users/me/subscribed-courses/${courseId}/lectures/${lectureId}/?fields[lecture]=asset&fields[asset]=captions`,
    `https://www.udemy.com/api-2.0/courses/${courseId}/subscriber-curriculum-items/${lectureId}/?fields[lecture]=asset&fields[asset]=captions`,
    `https://www.udemy.com/api-2.0/courses/${courseId}/lectures/${lectureId}/?fields[lecture]=asset&fields[asset]=captions`,
  ]

  for (const url of endpoints) {
    try {
      const res = await fetch(url, { credentials: 'include', headers: UDEMY_HEADERS })
      if (!res.ok) continue
      const data = await res.json()
      const captions: any[] = data?.asset?.captions ?? []
      if (captions.length === 0) continue
      const caption = captions.find((c: any) => c.locale_id?.startsWith(lang)) ?? captions[0]
      if (caption?.url) return caption.url as string
    } catch {
      continue
    }
  }

  throw new ExtractError('UDEMY_VTT_URL_FAILED', 'No caption URL from any Udemy endpoint')
}

async function tier1Api(parsed: ParsedUrl, lang: string): Promise<TranscriptEntry[]> {
  const courseId = await fetchCourseId(parsed.slug)
  const vttUrl = await fetchVttUrl(courseId, parsed.lectureId, lang)
  const res = await fetch(vttUrl)
  if (!res.ok) {
    throw new ExtractError('UDEMY_VTT_FETCH_FAILED', `VTT fetch failed: ${res.status}`)
  }
  const entries = parseVtt(await res.text())
  if (entries.length === 0) {
    throw new ExtractError('UDEMY_VTT_PARSE_EMPTY', 'VTT parsed but produced 0 entries')
  }
  return entries
}

async function tier2TextTrack(lang: string): Promise<TranscriptEntry[]> {
  const video = document.querySelector('video')
  if (!video) throw new ExtractError('NO_VIDEO_ELEMENT', 'No <video> element on the page')
  if (video.textTracks.length === 0) {
    throw new ExtractError('NO_TEXT_TRACKS', 'video has 0 textTracks — open subtitles first')
  }
  const track = await waitForCues(video.textTracks, 8000, lang)
  return cuesToEntries(track)
}

export class UdemyExtractor implements SubtitleExtractor {
  readonly site = 'udemy' as const

  videoKey(): string {
    const parsed = parseUrl()
    if (!parsed) return location.pathname
    return `udemy:${parsed.slug}/${parsed.lectureId}`
  }

  title(): string {
    const el = document.querySelector('[data-purpose="lecture-title"], h1')
    return el?.textContent?.trim() || document.title
  }

  async extractFullTranscript(preferredLang = 'en'): Promise<TranscriptEntry[]> {
    // Udemy renders captions in DOM (well--text), not in the browser's TextTracks
    // for [CC] tracks. Prefer Tier 1 REST API which fetches the same VTT the
    // player is using; observation is via DOM polling (see observeCurrentCue).
    const parsed = parseUrl()
    if (!parsed) {
      throw new ExtractError('NOT_LECTURE_PAGE', 'Not on a Udemy lecture page')
    }
    try {
      const entries = await tier1Api(parsed, preferredLang)
      console.log(`[DualSub] Udemy REST API → ${entries.length} cues`)
      return entries
    } catch (err) {
      console.warn('[DualSub] Udemy REST API failed, falling back to TextTrack:', err)
    }
    const entries = await tier2TextTrack(preferredLang)
    console.log(`[DualSub] Udemy TextTrack fallback → ${entries.length} cues`)
    return entries
  }

  observeCurrentCue(observer: CueObserver): () => void {
    return observeUdemyCaptions(observer)
  }
}
