import { afterEach, describe, expect, it, vi } from 'vitest'
import { DaemonClient, type LookupResponse, type ModelList } from './DaemonClient'

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

describe('DaemonClient.listModels', () => {
  afterEach(() => vi.unstubAllGlobals())

  const list: ModelList = {
    models: [{ id: 'gpt-5.6-sol', label: 'GPT-5.6-Sol', efforts: ['low'], default_effort: 'low' }],
    fetched_at: '2026-10-27T01:02:03Z',
  }

  it('GETs the encoded provider path and returns the body', async () => {
    const fetchMock = vi.fn(async () => jsonResponse(list))
    vi.stubGlobal('fetch', fetchMock)
    const res = await new DaemonClient('http://daemon.test').listModels('co dex')
    expect(res).toEqual(list)
    const [url] = fetchMock.mock.calls[0] as unknown as [string]
    expect(url).toBe('http://daemon.test/v1/providers/co%20dex/models')
  })

  it('adds ?refresh=1 when asked', async () => {
    const fetchMock = vi.fn(async () => jsonResponse(list))
    vi.stubGlobal('fetch', fetchMock)
    await new DaemonClient('http://daemon.test').listModels('codex', { refresh: true })
    const [url] = fetchMock.mock.calls[0] as unknown as [string]
    expect(url).toBe('http://daemon.test/v1/providers/codex/models?refresh=1')
  })

  it('throws the daemon error text on non-2xx', async () => {
    vi.stubGlobal('fetch', vi.fn(async () => jsonResponse({ error: 'agy is not signed in' }, 502)))
    await expect(new DaemonClient('http://daemon.test').listModels('agy')).rejects.toThrow('agy is not signed in')
  })

  it('falls back to the HTTP status when the error body is not JSON', async () => {
    vi.stubGlobal('fetch', vi.fn(async () => new Response('boom', { status: 500 })))
    await expect(new DaemonClient('http://daemon.test').listModels('agy')).rejects.toThrow('HTTP 500')
  })
})
