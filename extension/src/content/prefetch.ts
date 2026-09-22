import type { LookupRequest, LookupResponse, RemoteStatus } from '@/shared/DaemonClient'
import type { TranscriptEntry } from '@/shared/transcript'

/**
 * Page-load prefetch: find translations that already exist (locally or on the
 * central node) and show them. This path never translates; that decision is
 * left to the caller via needsTranslation().
 *
 * Dependencies are injected so the flow is testable without the content
 * script's module-level side effects.
 */
export interface PrefetchDeps {
  videoKey: string
  sourceLang: string
  targetLang: string
  extract: () => Promise<TranscriptEntry[]>
  lookup: (req: LookupRequest) => Promise<LookupResponse>
  /** Receives original text → translation for every hit. */
  apply: (translations: Record<string, string>) => void
  log?: (message: string) => void
}

export interface PrefetchResult {
  entries: TranscriptEntry[]
  hits: number
  total: number
  /** 'error' = the daemon lookup itself failed (offline, or too old for /v1/lookup). */
  remoteStatus: RemoteStatus | 'error'
}

/** True when sticky auto-translate still has lines to fill. */
export function needsTranslation(result: PrefetchResult | null): boolean {
  if (result === null) return true
  return result.hits < result.total
}

export async function prefetchCachedTranslations(deps: PrefetchDeps): Promise<PrefetchResult | null> {
  let entries: TranscriptEntry[]
  try {
    entries = await deps.extract()
  } catch (err) {
    deps.log?.(`prefetch: extract failed: ${err instanceof Error ? err.message : String(err)}`)
    return null
  }
  if (entries.length === 0) {
    return { entries, hits: 0, total: 0, remoteStatus: 'disabled' }
  }

  let res: LookupResponse
  try {
    res = await deps.lookup({
      video_key: deps.videoKey,
      source_lang: deps.sourceLang,
      target_lang: deps.targetLang,
      lines: entries.map((e) => ({ index: e.index, text: e.originalText })),
      include_remote: true,
    })
  } catch (err) {
    deps.log?.(`prefetch: lookup failed: ${err instanceof Error ? err.message : String(err)}`)
    return { entries, hits: 0, total: entries.length, remoteStatus: 'error' }
  }

  const byIndex = new Map<number, string>()
  for (const e of entries) byIndex.set(e.index, e.originalText)
  const translations: Record<string, string> = {}
  for (const t of res.translations) {
    const original = byIndex.get(t.index)
    if (original) translations[original] = t.text
  }
  if (Object.keys(translations).length > 0) deps.apply(translations)

  return { entries, hits: res.hits, total: res.total, remoteStatus: res.remote_status }
}
