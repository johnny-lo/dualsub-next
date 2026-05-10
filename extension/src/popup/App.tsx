import { useEffect, useMemo, useRef, useState } from 'react'
import {
  Alert,
  Button,
  Collapse,
  ConfigProvider,
  Divider,
  Input,
  Progress,
  Select,
  Space,
  Switch,
  Tag,
  Typography,
} from 'antd'
import {
  CheckOutlined,
  CopyOutlined,
  ReloadOutlined,
  SettingOutlined,
  StopOutlined,
  TranslationOutlined,
} from '@ant-design/icons'
import {
  formatTranscriptForExport,
  parseTranslatedTranscript,
  type TranscriptPayload,
} from '@/shared/transcript'
import {
  sendToActiveTab,
  type ContentMessage,
  type ExtractTranscriptResponse,
  type OverlayStatus,
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

// Path to the dualsub-next repo on this machine. Edit if you move the repo.
const DAEMON_DIR_WINDOWS = 'c:\\Users\\j4503\\repos\\dualsub-next'

function detectStartCommand(): string {
  const ua = navigator.userAgent
  const uaPlatform = (navigator as { userAgentData?: { platform?: string } }).userAgentData?.platform ?? ''
  const isWindows = /windows/i.test(uaPlatform) || /windows/i.test(ua)
  if (isWindows) {
    return `cd '${DAEMON_DIR_WINDOWS}'; .\\dualsub-watch.ps1`
  }
  // Linux/macOS — path unknown to extension; user runs from their repo root.
  return './dualsub-watch.sh'
}

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

  const [overlayStatus, setOverlayStatus] = useState<OverlayStatus | null>(null)

  const [extract, setExtract] = useState<ExtractState>({ state: 'idle' })
  const [job, setJob] = useState<TranslateJob | null>(null)
  const [recentJobs, setRecentJobs] = useState<JobSummary[]>([])
  const abortRef = useRef<(() => void) | null>(null)

  const startCommand = useMemo(() => detectStartCommand(), [])
  const [copiedStart, setCopiedStart] = useState(false)
  const onCopyStartCommand = async () => {
    try {
      await navigator.clipboard.writeText(startCommand)
      setCopiedStart(true)
      setTimeout(() => setCopiedStart(false), 1500)
    } catch (err) {
      console.error('[DualSub] clipboard write failed:', err)
    }
  }

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

  const refreshOverlayStatus = async () => {
    try {
      const status = await sendToActiveTab<OverlayStatus>({
        type: 'PING_OVERLAY',
      } satisfies ContentMessage)
      if (status?.ok) setOverlayStatus(status)
    } catch {
      setOverlayStatus(null)
    }
  }

  useEffect(() => {
    void refreshDaemon()
    void refreshOverlayStatus()
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

  // ─── paste-back translated text ─────────────────────────────────────────

  const [pasteText, setPasteText] = useState('')
  const [pasteResult, setPasteResult] = useState<{ ok: boolean; message: string } | null>(null)

  const onApplyPasted = async () => {
    if (!pasteText.trim()) return
    const parsed = parseTranslatedTranscript(pasteText)
    if (parsed.length === 0) {
      setPasteResult({ ok: false, message: 'No [index] lines found. Use [1] text format.' })
      return
    }
    // We need the source payload to map index → original text
    const sourcePayload = extract.state === 'copied' ? extract.payload : job?.source
    if (!sourcePayload) {
      setPasteResult({ ok: false, message: 'Extract subtitles first so we can match indices.' })
      return
    }
    const byIndex = new Map<number, string>()
    for (const e of sourcePayload.entries) byIndex.set(e.index, e.originalText)
    const translations: Record<string, string> = {}
    for (const p of parsed) {
      const orig = byIndex.get(p.index)
      if (orig) translations[orig] = p.translatedText
    }
    try {
      await sendToActiveTab<SimpleAck>({
        type: 'SET_OVERLAY',
        videoKey: sourcePayload.videoKey,
        translations,
      } satisfies ContentMessage)
      setPasteResult({ ok: true, message: `Applied ${Object.keys(translations).length} translations to overlay.` })
      void refreshOverlayStatus()
    } catch (err) {
      setPasteResult({ ok: false, message: (err as Error).message })
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
          setJob((j) =>
            j && {
              ...j,
              totalChunks: info.total_chunks,
              totalLines: info.total_lines,
              cacheHits: info.cache_hits,
            },
          ),
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
        onDone: (done) => {
          setJob((j) =>
            j && {
              ...j,
              status: 'done',
              completedChunks: done.completed,
            },
          )
          void client.listJobs(5).then(setRecentJobs).catch(() => {})
          void refreshOverlayStatus()
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

  const translatedLineCount = useMemo(() => {
    if (!job) return 0
    return new Set(job.translated.map((line) => line.index)).size
  }, [job])

  const progressPct = useMemo(() => {
    if (!job || job.totalLines === 0) return 0
    return Math.min(100, Math.round((translatedLineCount / job.totalLines) * 100))
  }, [job, translatedLineCount])

  return (
    <ConfigProvider>
      <div style={{ width: 380, padding: 16 }}>
        <Typography.Title level={5} style={{ marginTop: 0 }}>
          DualSub Next
        </Typography.Title>

        {/* Daemon status */}
        <Space direction="vertical" size="small" style={{ width: '100%' }}>
          <Space style={{ width: '100%', justifyContent: 'space-between' }}>
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
                title="Refresh"
              />
            </Space>
            <Button
              icon={<SettingOutlined />}
              onClick={() => chrome.runtime.openOptionsPage()}
              size="small"
              type="text"
              title="Open settings"
            >
              Settings
            </Button>
          </Space>
          {daemon.state === 'offline' && (
            <Space direction="vertical" size={4} style={{ width: '100%' }}>
              <Typography.Text type="danger" style={{ fontSize: 12 }}>
                {daemon.error}
              </Typography.Text>
              <Typography.Text type="secondary" style={{ fontSize: 11 }}>
                Run this in a terminal to start the daemon:
              </Typography.Text>
              <Space.Compact style={{ width: '100%' }}>
                <Input
                  readOnly
                  size="small"
                  value={startCommand}
                  style={{ fontFamily: 'monospace', fontSize: 11 }}
                />
                <Button
                  size="small"
                  icon={<CopyOutlined />}
                  onClick={onCopyStartCommand}
                  title="Copy command"
                />
              </Space.Compact>
              {copiedStart && (
                <Typography.Text type="success" style={{ fontSize: 11 }}>
                  Copied — paste into a terminal and press Enter.
                </Typography.Text>
              )}
            </Space>
          )}
        </Space>

        {/* Current video translation status */}
        <div style={{ marginTop: 10, padding: '6px 10px', background: '#f5f5f5', borderRadius: 4 }}>
          <Space size="small">
            <span style={{ fontSize: 12 }}>Current video:</span>
            {overlayStatus === null ? (
              <Tag color="default">no subtitle page</Tag>
            ) : overlayStatus.translationsCount > 0 ? (
              <Tag color="green">translated ({overlayStatus.translationsCount} lines)</Tag>
            ) : overlayStatus.mounted ? (
              <Tag color="orange">overlay active, no translations</Tag>
            ) : (
              <Tag color="default">not translated</Tag>
            )}
            {overlayStatus?.liveMode && <Tag color="blue">Live</Tag>}
          </Space>
        </div>

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
                    {job.totalChunks > 0
                      ? `${job.completedChunks}/${job.totalChunks} chunks`
                      : 'cache only'}
                    {job.cacheHits > 0 ? ` · ${job.cacheHits} cached` : ''}
                  </span>
                  <span>{translatedLineCount} / {job.totalLines} lines</span>
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

        <div style={{ marginTop: 10 }}>
          <Typography.Text type="secondary" style={{ fontSize: 12 }}>
            Paste translated result here (keep [index] format):
          </Typography.Text>
          <Input.TextArea
            rows={4}
            value={pasteText}
            onChange={(e) => { setPasteText(e.target.value); setPasteResult(null) }}
            placeholder={'[1] 翻譯後的文字\n[2] 第二行翻譯\n...'}
            style={{ marginTop: 4, fontSize: 12 }}
          />
          <Button
            icon={<CheckOutlined />}
            onClick={onApplyPasted}
            disabled={!pasteText.trim()}
            block
            style={{ marginTop: 6 }}
          >
            Apply to overlay
          </Button>
          {pasteResult && (
            <Alert
              type={pasteResult.ok ? 'success' : 'error'}
              showIcon
              message={pasteResult.message}
              style={{ marginTop: 6 }}
            />
          )}
        </div>
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
