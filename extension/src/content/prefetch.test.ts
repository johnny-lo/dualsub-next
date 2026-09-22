import { describe, expect, it, vi } from 'vitest'
import type { LookupResponse } from '@/shared/DaemonClient'
import { needsTranslation, prefetchCachedTranslations, type PrefetchDeps } from './prefetch'

const entries = [
  { index: 1, originalText: 'Hello' },
  { index: 2, originalText: 'World' },
]

function deps(overrides: Partial<PrefetchDeps> = {}): PrefetchDeps & {
  apply: ReturnType<typeof vi.fn>
  lookup: ReturnType<typeof vi.fn>
} {
  const lookup = vi.fn(
    async (): Promise<LookupResponse> => ({
      translations: [{ index: 1, text: '你好' }],
      hits: 1,
      total: 2,
      remote_hits: 0,
      remote_status: 'disabled',
    }),
  )
  const apply = vi.fn()
  return {
    videoKey: 'udemy:course/1',
    sourceLang: 'en',
    targetLang: '繁體中文',
    extract: async () => entries,
    lookup,
    apply,
    ...overrides,
  } as PrefetchDeps & {
    apply: ReturnType<typeof vi.fn>
    lookup: ReturnType<typeof vi.fn>
  }
}

describe('prefetchCachedTranslations', () => {
  it('applies hits keyed by original text and reports coverage', async () => {
    const d = deps()
    const result = await prefetchCachedTranslations(d)
    expect(d.apply).toHaveBeenCalledWith({ Hello: '你好' })
    expect(result).toEqual({ entries, hits: 1, total: 2, remoteStatus: 'disabled', videoKey: 'udemy:course/1' })
    expect(d.lookup).toHaveBeenCalledWith({
      video_key: 'udemy:course/1',
      source_lang: 'en',
      target_lang: '繁體中文',
      lines: [
        { index: 1, text: 'Hello' },
        { index: 2, text: 'World' },
      ],
      include_remote: true,
    })
  })

  it('does not touch the overlay when nothing hits', async () => {
    const d = deps({
      lookup: vi.fn(async () => ({
        translations: [],
        hits: 0,
        total: 2,
        remote_hits: 0,
        remote_status: 'ok' as const,
      })),
    })
    const result = await prefetchCachedTranslations(d)
    expect(d.apply).not.toHaveBeenCalled()
    expect(result?.hits).toBe(0)
  })

  it('returns null and skips the lookup when extraction fails', async () => {
    const d = deps({
      extract: async () => {
        throw new Error('no transcript')
      },
    })
    expect(await prefetchCachedTranslations(d)).toBeNull()
    expect(d.lookup).not.toHaveBeenCalled()
  })

  it('skips the lookup for an empty transcript', async () => {
    const d = deps({ extract: async () => [] })
    expect(await prefetchCachedTranslations(d)).toEqual({
      entries: [],
      hits: 0,
      total: 0,
      remoteStatus: 'disabled',
      videoKey: 'udemy:course/1',
    })
    expect(d.lookup).not.toHaveBeenCalled()
  })

  it('keeps the entries when the daemon lookup fails so auto-translate can still run', async () => {
    const log = vi.fn()
    const d = deps({
      lookup: vi.fn(async () => {
        throw new Error('HTTP 404')
      }),
      log,
    })
    const result = await prefetchCachedTranslations(d)
    expect(result).toEqual({ entries, hits: 0, total: 2, remoteStatus: 'error', videoKey: 'udemy:course/1' })
    expect(d.apply).not.toHaveBeenCalled()
    expect(log).toHaveBeenCalledWith(expect.stringContaining('HTTP 404'))
  })

  it('still returns coverage when apply throws, and logs the failure', async () => {
    const log = vi.fn()
    const apply = vi.fn(() => {
      throw new Error('overlay not ready')
    })
    const d = deps({ apply, log })
    const result = await prefetchCachedTranslations(d)
    expect(result).toEqual({ entries, hits: 1, total: 2, remoteStatus: 'disabled', videoKey: 'udemy:course/1' })
    expect(log).toHaveBeenCalledWith(expect.stringContaining('overlay not ready'))
  })
})

describe('needsTranslation', () => {
  it('is true when prefetch could not run', () => {
    expect(needsTranslation(null)).toBe(true)
  })
  it('is true when some lines are missing', () => {
    expect(
      needsTranslation({ entries, hits: 1, total: 2, remoteStatus: 'ok', videoKey: 'udemy:course/1' }),
    ).toBe(true)
  })
  it('is false when every line was found', () => {
    expect(
      needsTranslation({ entries, hits: 2, total: 2, remoteStatus: 'ok', videoKey: 'udemy:course/1' }),
    ).toBe(false)
  })
  it('is false for an empty transcript (nothing to translate)', () => {
    expect(
      needsTranslation({ entries: [], hits: 0, total: 0, remoteStatus: 'disabled', videoKey: 'udemy:course/1' }),
    ).toBe(false)
  })
})
