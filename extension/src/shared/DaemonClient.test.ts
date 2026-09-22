import { afterEach, describe, expect, it, vi } from 'vitest'
import { DaemonClient, type LookupResponse } from './DaemonClient'

function jsonResponse(body: unknown, status = 200): Response {
  return new Response(JSON.stringify(body), {
    status,
    headers: { 'Content-Type': 'application/json' },
  })
}

describe('DaemonClient.lookup', () => {
  afterEach(() => vi.unstubAllGlobals())

  it('posts a cache-only lookup and returns the parsed body', async () => {
    const payload: LookupResponse = {
      translations: [{ index: 1, text: '你好' }],
      hits: 1,
      total: 2,
      remote_hits: 0,
      remote_status: 'disabled',
    }
    const fetchMock = vi.fn(async () => jsonResponse(payload))
    vi.stubGlobal('fetch', fetchMock)

    const res = await new DaemonClient('http://daemon.test').lookup({
      video_key: 'udemy:course/1',
      source_lang: 'en',
      target_lang: '繁體中文',
      lines: [
        { index: 1, text: 'Hello' },
        { index: 2, text: 'World' },
      ],
      include_remote: true,
    })

    expect(res).toEqual(payload)
    const [url, init] = fetchMock.mock.calls[0] as unknown as [string, RequestInit]
    expect(url).toBe('http://daemon.test/v1/lookup')
    expect(init.method).toBe('POST')
    expect(JSON.parse(init.body as string)).toMatchObject({ include_remote: true })
  })

  it('bounds the request with a 10s abort signal, matching the daemon-side timeout', async () => {
    const fetchMock = vi.fn(async () =>
      jsonResponse({ translations: [], hits: 0, total: 1, remote_hits: 0, remote_status: 'disabled' }),
    )
    vi.stubGlobal('fetch', fetchMock)

    await new DaemonClient('http://daemon.test').lookup({
      source_lang: 'en',
      target_lang: '繁體中文',
      lines: [{ index: 1, text: 'Hello' }],
      include_remote: false,
    })

    const [, init] = fetchMock.mock.calls[0] as unknown as [string, RequestInit]
    expect(init.signal).toBeInstanceOf(AbortSignal)
  })

  it('throws with the status on a non-2xx response', async () => {
    vi.stubGlobal('fetch', vi.fn(async () => new Response('too many lines', { status: 400 })))
    await expect(
      new DaemonClient('http://daemon.test').lookup({
        source_lang: 'en',
        target_lang: '繁體中文',
        lines: [{ index: 1, text: 'x' }],
        include_remote: false,
      }),
    ).rejects.toThrow('HTTP 400')
  })
})
