import { useEffect, useMemo, useRef, useState } from 'react'
import {
  Alert,
  Button,
  Collapse,
  ConfigProvider,
  Divider,
  Progress,
  Select,
  Space,
  Switch,
  Tag,
  Typography,
} from 'antd'
import {
  CopyOutlined,
  ReloadOutlined,
  StopOutlined,
  TranslationOutlined,
} from '@ant-design/icons'
import {
  formatTranscriptForExport,
  type TranscriptPayload,
} from '@/shared/transcript'
import {
  sendToActiveTab,
  type ContentMessage,
  type ExtractTranscriptResponse,
  type SimpleAck,
} from '@/shared/messaging'
import {
  DaemonClient,
  type ChunkErrorPayload,
  type JobSummary,
  type ProviderInfo,
  type TranslatedLine,
} from '@/shared/DaemonClient'

const TARGET_LANG = '繁體中文'
const PREFERRED_SOURCE = 'en'

type DaemonStatus =
  | { state: 'checking' }
  | { state: 'connected'; serverTime: string }
  | { state: 'offline'; error: string }

type ExtractState =
  | { state: 'idle' }
  | { state: 'working' }
  | { state: 'copied'; entries: number; payload: TranscriptPayload }
  | { state: 'error'; code: string; message: string }

type TranslateJob = {
  status: 'extracting' | 'running' | 'done' | 'aborted'
  totalChunks: number
  completedChunks: number
  cacheHits: number
  totalLines: number
  errors: ChunkErrorPayload[]
  translated: TranslatedLine[]
  fatal?: string
  source?: TranscriptPayload
}

const client = new DaemonClient()

// Build a Record<original_text, translated_text> from a chunk payload.
function makeChunkMap(
  source: TranscriptPayload,
  lines: TranslatedLine[],
): Record<string, string> {
  const byIndex = new Map<number, string>()
  for (const e of source.entries) byIndex.set(e.index, e.originalText)
  const out: Record<string, string> = {}
  for (const l of lines) {
    const orig = byIndex.get(l.index)
    if (orig) out[orig] = l.text
  }
  return out
}

export default function App() {
  const [daemon, setDaemon] = useState<DaemonStatus>({ state: 'checking' })
  const [providers, setProviders] = useState<ProviderInfo[]>([])
  const [providerLoadError, setProviderLoadError] = useState<string | null>(null)
  const [chosen, setChosen] = useState<string>('')
  const [liveMode, setLiveMode] = useState(false)

  const [extract, setExtract] = useState<ExtractState>({ state: 'idle' })
  const [job, setJob] = useState<TranslateJob | null>(null)
  const [recentJobs, setRecentJobs] = useState<JobSummary[]>([])
  const abortRef = useRef<(() => void) | null>(null)

  const refreshDaemon = async () => {
    setDaemon({ state: 'checking' })
    try {
      const h = await client.health()
      setDaemon({ state: 'connected', serverTime: h.time })
      try {
        const list = await client.listProviders()
        setProviders(list)
        setProviderLoadError(null)
        if (list.length > 0 && !chosen) setChosen(list[0].name)
      } catch (err) {
        setProviderLoadError((err as Error).message)
      }
      try {
        setRecentJobs(await client.listJobs(5))
      } catch {
        // ignore — non-essential
      }
    } catch (err) {
      setDaemon({ state: 'offline', error: (err as Error).message })
    }
  }

  useEffect(() => {
    void refreshDaemon()
    return () => abortRef.current?.()
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [])

  // ─── one-click extract original (fallback) ───────────────────────────────

  const onExtract = async () => {
    setExtract({ state: 'working' })
    try {
      const res = await sendToActiveTab<ExtractTranscriptResponse>({
        type: 'EXTRACT_TRANSCRIPT',
        preferredLang: PREFERRED_SOURCE,
      } satisfies ContentMessage)

      if (!res?.ok) {
        setExtract({
          state: 'error',
          code: res?.code ?? 'UNKNOWN',
          message: res?.message ?? 'No response from content script',
        })
        return
      }
      const text = formatTranscriptForExport(res.payload.entries, TARGET_LANG)
      await navigator.clipboard.writeText(text)
      setExtract({ state: 'copied', entries: res.payload.entries.length, payload: res.payload })
    } catch (err) {
      setExtract({ state: 'error', code: 'MESSAGING_FAILED', message: (err as Error).message })
    }
  }

  // ─── translate via daemon ────────────────────────────────────────────────

  const onTranslate = async () => {
    if (!chosen) return
    abortRef.current?.()
    setJob({
      status: 'extracting',
      totalChunks: 0,
      completedChunks: 0,
      cacheHits: 0,
      totalLines: 0,
      errors: [],
      translated: [],
    })

    let payload: TranscriptPayload
    try {
      const res = await sendToActiveTab<ExtractTranscriptResponse>({
        type: 'EXTRACT_TRANSCRIPT',
        preferredLang: PREFERRED_SOURCE,
      } satisfies ContentMessage)
      if (!res?.ok) {
        setJob((j) => j && { ...j, status: 'done', fatal: `${res?.code}: ${res?.message}` })
        return
      }
      payload = res.payload
    } catch (err) {
      setJob((j) => j && { ...j, status: 'done', fatal: (err as Error).message })
      return
    }

    // Mount overlay with empty translations so cues start showing originals
    // immediately; chunks will patch in translations as they arrive.
    try {
      await sendToActiveTab<SimpleAck>({
        type: 'SET_OVERLAY',
        videoKey: payload.videoKey,
        translations: {},
      } satisfies ContentMessage)
    } catch {
      // overlay is best-effort; don't abort the translate
    }

    setJob((j) => j && { ...j, source: payload, status: 'running', totalLines: payload.entries.length })

    abortRef.current = client.translate(
      {
        site: payload.site,
        video_key: payload.videoKey,
        title: payload.title,
        provider: chosen,
        source_lang: PREFERRED_SOURCE,
        target_lang: TARGET_LANG,
        lines: payload.entries.map((e) => ({ index: e.index, text: e.originalText })),
      },
      {
        onJobCreated: (info) =>
          setJob((j) => j && { ...j, totalChunks: info.total_chunks, cacheHits: info.cache_hits }),
        onChunkDone: (ck) => {
          setJob((j) => {
            if (!j) return j
            return {
              ...j,
              completedChunks: j.completedChunks + (ck.source === 'llm' ? 1 : 0),
              translated: [...j.translated, ...ck.lines],
            }
          })
          // patch overlay with this chunk's translations
          void sendToActiveTab<SimpleAck>({
            type: 'PATCH_OVERLAY',
            videoKey: payload.videoKey,
            translations: makeChunkMap(payload, ck.lines),
          } satisfies ContentMessage).catch(() => {})
        },
        onChunkError: (err) =>
          setJob((j) => {
            if (!j) return j
            if (!err.final) return j
            return { ...j, errors: [...j.errors, err] }
          }),
        onDone: () => {
          setJob((j) => j && { ...j, status: 'done' })
          void client.listJobs(5).then(setRecentJobs).catch(() => {})
        },
        onFatal: (err) =>
          setJob((j) => j && { ...j, status: 'done', fatal: err.message }),
      },
    )
  }

  const onAbort = () => {
    abortRef.current?.()
    abortRef.current = null
    setJob((j) => j && { ...j, status: 'aborted' })
  }

  const onClearOverlay = async () => {
    try {
      await sendToActiveTab<SimpleAck>({ type: 'CLEAR_OVERLAY' } satisfies ContentMessage)
    } catch {
      // ignore
    }
  }

  const onLiveModeToggle = async (enabled: boolean) => {
    setLiveMode(enabled)
    if (!chosen && enabled) return
    try {
      await sendToActiveTab<SimpleAck>({
        type: 'SET_LIVE_MODE',
        enabled,
        provider: chosen,
        targetLang: TARGET_LANG,
      } satisfies ContentMessage)
    } catch {
      setLiveMode(!enabled)
    }
  }

  const onCopyTranslated = async () => {
    if (!job) return
    const sorted = [...job.translated].sort((a, b) => a.index - b.index)
    const text = sorted.map((l) => `[${l.index}] ${l.text}`).join('\n')
    await navigator.clipboard.writeText(text)
  }

  const progressPct = useMemo(() => {
    if (!job || job.totalChunks === 0) return 0
    return Math.round((job.completedChunks / job.totalChunks) * 100)
  }, [job])

  return (
    <ConfigProvider>
      <div style={{ width: 380, padding: 16 }}>
        <Typography.Title level={5} style={{ marginTop: 0 }}>
          DualSub Next
        </Typography.Title>

        {/* Daemon status */}
        <Space direction="vertical" size="small" style={{ width: '100%' }}>
          <Space>
            <span>Daemon:</span>
            {daemon.state === 'checking' && <Tag color="blue">checking…</Tag>}
            {daemon.state === 'connected' && <Tag color="green">connected</Tag>}
            {daemon.state === 'offline' && <Tag color="red">offline</Tag>}
            <Button
              icon={<ReloadOutlined />}
              onClick={refreshDaemon}
              size="small"
              type="text"
            />
          </Space>
          {daemon.state === 'offline' && (
            <Typography.Text type="danger" style={{ fontSize: 12 }}>
              {daemon.error} — run <code>./dualsub serve</code>
            </Typography.Text>
          )}
        </Space>

        <Divider style={{ margin: '14px 0 10px' }} />

        {/* Translate */}
        <Typography.Text strong>Translate full transcript</Typography.Text>
        <div style={{ marginTop: 8 }}>
          <Space.Compact style={{ width: '100%' }}>
            <Select
              value={chosen}
              onChange={setChosen}
              disabled={providers.length === 0}
              style={{ width: 130 }}
              placeholder="Provider"
              options={providers.map((p) => ({ value: p.name, label: p.name }))}
            />
            <Button
              type="primary"
              icon={<TranslationOutlined />}
              onClick={onTranslate}
              disabled={
                daemon.state !== 'connected' ||
                providers.length === 0 ||
                job?.status === 'running' ||
                job?.status === 'extracting'
              }
              block
            >
              Translate
            </Button>
          </Space.Compact>
          {providerLoadError && (
            <Typography.Text type="danger" style={{ fontSize: 12 }}>
              providers: {providerLoadError}
            </Typography.Text>
          )}
          {providers.length === 0 && daemon.state === 'connected' && !providerLoadError && (
            <Typography.Text type="warning" style={{ fontSize: 12 }}>
              No providers configured. Open Options to set up.
            </Typography.Text>
          )}
        </div>

        {job && (
          <div style={{ marginTop: 10 }}>
            {job.status === 'extracting' && (
              <Typography.Text type="secondary" style={{ fontSize: 12 }}>
                Extracting transcript from page…
              </Typography.Text>
            )}
            {(job.status === 'running' || job.status === 'done' || job.status === 'aborted') && (
              <>
                <div style={{ display: 'flex', justifyContent: 'space-between', fontSize: 12 }}>
                  <span>
                    {job.completedChunks}/{job.totalChunks} chunks
                    {job.cacheHits > 0 ? ` · ${job.cacheHits} cached` : ''}
                  </span>
                  <span>{job.translated.length} / {job.totalLines} lines</span>
                </div>
                <Progress
                  percent={progressPct}
                  size="small"
                  status={
                    job.fatal
                      ? 'exception'
                      : job.status === 'done' && job.errors.length > 0
                        ? 'exception'
                        : job.status === 'done'
                          ? 'success'
                          : 'active'
                  }
                />
              </>
            )}
            {job.status === 'running' && (
              <Button onClick={onAbort} icon={<StopOutlined />} size="small" block>
                Cancel
              </Button>
            )}
            {job.fatal && (
              <Alert type="error" showIcon message="Translate failed" description={job.fatal} />
            )}
            {job.errors.length > 0 && (
              <Collapse
                size="small"
                style={{ marginTop: 6 }}
                items={[
                  {
                    key: 'errs',
                    label: `${job.errors.length} chunk error(s)`,
                    children: job.errors.map((e, i) => (
                      <div key={i} style={{ fontSize: 11 }}>
                        <Tag color="red">chunk {e.chunk}</Tag> <code>{e.code}</code>: {e.message}
                      </div>
                    )),
                  },
                ]}
              />
            )}
            {job.status === 'done' && job.translated.length > 0 && (
              <Space style={{ marginTop: 6, width: '100%' }} direction="vertical" size="small">
                <Button size="small" icon={<CopyOutlined />} onClick={onCopyTranslated} block>
                  Copy translated ({job.translated.length} lines)
                </Button>
                <Button size="small" onClick={onClearOverlay} block>
                  Hide overlay on page
                </Button>
              </Space>
            )}
          </div>
        )}

        <Divider style={{ margin: '14px 0 10px' }} />

        {/* Live mode */}
        <Space style={{ width: '100%', justifyContent: 'space-between' }}>
          <span>
            <Typography.Text strong>Live mode</Typography.Text>
            <Typography.Text type="secondary" style={{ fontSize: 11, marginLeft: 6 }}>
              translate cues as they appear
            </Typography.Text>
          </span>
          <Switch
            checked={liveMode}
            onChange={onLiveModeToggle}
            disabled={daemon.state !== 'connected' || providers.length === 0}
          />
        </Space>

        <Divider style={{ margin: '14px 0 10px' }} />

        {/* Recent jobs (M7) */}
        {recentJobs.length > 0 && (
          <Collapse
            size="small"
            items={[
              {
                key: 'rj',
                label: `Recent jobs (${recentJobs.length})`,
                children: recentJobs.map((j) => (
                  <div key={j.job_id} style={{ fontSize: 11, marginBottom: 4 }}>
                    <Tag color={statusColor(j.status)}>{j.status}</Tag>
                    {j.video_key} · {j.provider} · {j.completed_chunks}/{j.total_chunks}
                    {j.error_summary ? ` · ${j.error_summary}` : ''}
                  </div>
                )),
              },
            ]}
          />
        )}

        <Divider style={{ margin: '14px 0 10px' }} />

        {/* Extract original — fallback */}
        <Typography.Text strong>Fallback: copy original</Typography.Text>
        <Typography.Paragraph type="secondary" style={{ fontSize: 12, marginTop: 4 }}>
          For when translation fails — paste into ChatGPT / Gemini web.
        </Typography.Paragraph>
        <Space direction="vertical" size="small" style={{ width: '100%' }}>
          <Button
            icon={<CopyOutlined />}
            onClick={onExtract}
            loading={extract.state === 'working'}
            block
          >
            Extract & Copy ({TARGET_LANG})
          </Button>
          {extract.state === 'copied' && (
            <Alert
              type="success"
              showIcon
              message={`Copied ${extract.entries} lines`}
              description={extract.payload.title}
            />
          )}
          {extract.state === 'error' && (
            <Alert type="error" showIcon message={extract.code} description={extract.message} />
          )}
        </Space>
      </div>
    </ConfigProvider>
  )
}

function statusColor(status: string): string {
  switch (status) {
    case 'completed':
      return 'green'
    case 'partial':
      return 'orange'
    case 'failed':
      return 'red'
    case 'running':
      return 'blue'
    default:
      return 'default'
  }
}
