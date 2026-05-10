export const DAEMON_URL = 'http://127.0.0.1:7878'

export interface ProviderInfo {
  name: string
  default_model?: string
}

export interface TranslatedLine {
  index: number
  text: string
}

export interface TranslateRequest {
  site: string
  video_key: string
  title?: string
  provider: string
  model?: string
  source_lang: string
  target_lang: string
  lines: Array<{ index: number; text: string }>
}

export interface JobCreatedPayload {
  job_id: string
  total_chunks: number
  total_lines: number
  cache_hits: number
}

export interface ChunkDonePayload {
  chunk: number
  source: 'cache' | 'llm'
  lines: TranslatedLine[]
}

export interface ChunkErrorPayload {
  chunk: number
  code: string
  message: string
  retryable: boolean
  attempt: number
  final: boolean
}

export interface DonePayload {
  job_id: string
  total: number
  completed: number
  failed: number
  cache_hits: number
}

export interface FatalPayload {
  code: string
  message: string
}

export interface TranslateHandlers {
  onJobCreated?: (p: JobCreatedPayload) => void
  onChunkDone?: (p: ChunkDonePayload) => void
  onChunkError?: (p: ChunkErrorPayload) => void
  onDone?: (p: DonePayload) => void
  onFatal?: (err: Error) => void
}

export interface JobSummary {
  job_id: string
  video_key: string
  provider: string
  model: string
  status: string
  total_chunks: number
  completed_chunks: number
  failed_chunks: number
  error_summary: string
  created_at: number
  completed_at: number | null
}

export interface DaemonConfig {
  server: { listen: string }
  translate: { chunk_size: number; concurrency: number; max_attempts: number }
  cache: { path: string }
  providers: {
    openai?: { api_key: string; base_url: string; default_model: string }
    gemini?: { api_key: string; base_url: string; default_model: string }
    ollama?: { base_url: string; default_model: string }
  }
}

export class DaemonClient {
  constructor(private readonly baseURL: string = DAEMON_URL) {}

  async health(): Promise<{ status: string; time: string }> {
    const res = await fetch(`${this.baseURL}/healthz`)
    if (!res.ok) throw new Error(`HTTP ${res.status}`)
    return res.json()
  }

  async listProviders(): Promise<ProviderInfo[]> {
    const res = await fetch(`${this.baseURL}/v1/providers`)
    if (!res.ok) throw new Error(`HTTP ${res.status}`)
    return res.json()
  }

  async listJobs(limit = 10): Promise<JobSummary[]> {
    const res = await fetch(`${this.baseURL}/v1/jobs?limit=${limit}`)
    if (!res.ok) throw new Error(`HTTP ${res.status}`)
    return res.json()
  }

  async getConfig(): Promise<DaemonConfig> {
    const res = await fetch(`${this.baseURL}/v1/config`)
    if (!res.ok) throw new Error(`HTTP ${res.status}`)
    return res.json()
  }

  async putConfig(cfg: DaemonConfig): Promise<void> {
    const res = await fetch(`${this.baseURL}/v1/config`, {
      method: 'PUT',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify(cfg),
    })
    if (!res.ok) {
      const text = await res.text().catch(() => '')
      throw new Error(`HTTP ${res.status}: ${text}`)
    }
  }

  /**
   * Streams a translation job. Returns an abort function the caller can use
   * to cancel mid-stream (e.g., when the popup closes).
   */
  translate(req: TranslateRequest, handlers: TranslateHandlers): () => void {
    const controller = new AbortController()
    void this.runStream(req, handlers, controller.signal)
    return () => controller.abort()
  }

  private async runStream(
    req: TranslateRequest,
    handlers: TranslateHandlers,
    signal: AbortSignal,
  ): Promise<void> {
    let res: Response
    try {
      res = await fetch(`${this.baseURL}/v1/translate`, {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify(req),
        signal,
      })
    } catch (err) {
      if ((err as Error).name === 'AbortError') return
      handlers.onFatal?.(err as Error)
      return
    }

    if (!res.ok || !res.body) {
      const text = await res.text().catch(() => '')
      handlers.onFatal?.(new Error(`HTTP ${res.status}: ${text}`))
      return
    }

    const reader = res.body.getReader()
    const decoder = new TextDecoder()
    let buffer = ''

    try {
      while (true) {
        const { done, value } = await reader.read()
        if (done) break
        buffer += decoder.decode(value, { stream: true })

        let idx: number
        while ((idx = buffer.indexOf('\n\n')) !== -1) {
          const raw = buffer.slice(0, idx)
          buffer = buffer.slice(idx + 2)
          const parsed = parseSSEEvent(raw)
          if (parsed) dispatch(parsed, handlers)
        }
      }
    } catch (err) {
      if ((err as Error).name !== 'AbortError') {
        handlers.onFatal?.(err as Error)
      }
    }
  }
}

function parseSSEEvent(raw: string): { event: string; data: unknown } | null {
  let event = 'message'
  const dataLines: string[] = []
  for (const line of raw.split('\n')) {
    if (line.startsWith('event:')) {
      event = line.slice(6).trim()
    } else if (line.startsWith('data:')) {
      dataLines.push(line.slice(5).replace(/^ /, ''))
    }
  }
  if (dataLines.length === 0) return null
  try {
    return { event, data: JSON.parse(dataLines.join('\n')) }
  } catch {
    return null
  }
}

function dispatch(
  parsed: { event: string; data: unknown },
  h: TranslateHandlers,
): void {
  switch (parsed.event) {
    case 'job-created':
      h.onJobCreated?.(parsed.data as JobCreatedPayload)
      break
    case 'chunk-done':
      h.onChunkDone?.(parsed.data as ChunkDonePayload)
      break
    case 'chunk-error':
      h.onChunkError?.(parsed.data as ChunkErrorPayload)
      break
    case 'done':
      h.onDone?.(parsed.data as DonePayload)
      break
    case 'fatal': {
      const p = parsed.data as FatalPayload
      h.onFatal?.(new Error(`${p.code}: ${p.message}`))
      break
    }
  }
}
