import { useEffect, useMemo, useState, type CSSProperties } from 'react'
import {
  Alert,
  Button,
  Collapse,
  ConfigProvider,
  Input,
  Popconfirm,
  Progress,
  Select,
  Space,
  Switch,
  Tag,
  Tooltip,
  Typography,
} from 'antd'
import {
  CheckOutlined,
  CopyOutlined,
  DeleteOutlined,
  EyeOutlined,
  EyeInvisibleOutlined,
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
  type TranslateStatus,
} from '@/shared/messaging'
import {
  DaemonClient,
  type ChunkErrorPayload,
  type JobSummary,
  type ProviderInfo,
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
  failedChunks: number
  cacheHits: number
  totalLines: number
  translatedLineCount: number
  errors: ChunkErrorPayload[]
  fatal?: string
  videoKey?: string
  provider?: string
  source?: TranscriptPayload
}

const client = new DaemonClient()

const panelStyle: CSSProperties = {
  border: '1px solid #e5e7eb',
  borderRadius: 8,
  padding: 12,
  background: '#fff',
}

const mutedPanelStyle: CSSProperties = {
  ...panelStyle,
  background: '#f8fafc',
}

const sectionTitleStyle: CSSProperties = {
  display: 'flex',
  alignItems: 'center',
  justifyContent: 'space-between',
  gap: 8,
  marginBottom: 8,
}

function buildStartCommand(installDir: string | null): string {
  const ua = navigator.userAgent
  const uaPlatform = (navigator as { userAgentData?: { platform?: string } }).userAgentData?.platform ?? ''
  const isWindows = /windows/i.test(uaPlatform) || /windows/i.test(ua)
  if (isWindows) {
    const script = '.\\dualsub-watch.ps1'
    return installDir ? `cd '${installDir}'; ${script}` : script
  }
  const script = './dualsub-watch.sh'
  return installDir ? `cd '${installDir}' && ${script}` : script
}

function compactVideoKey(videoKey?: string | null): string {
  if (!videoKey) return 'No current video'
  return videoKey.replace(/^udemy:/, 'Udemy · ').replace(/^netflix:/, 'Netflix · ')
}

function jobFromTranslateStatus(status: TranslateStatus): TranslateJob | null {
  if (!status.active) return null
  const terminal = status.status === 'completed' || status.status === 'partial' || status.status === 'failed'
  const error = status.errorSummary
    ? [{
        chunk: 0,
        code: status.status.toUpperCase(),
        message: status.errorSummary,
        retryable: false,
        attempt: 1,
        final: true,
      }]
    : []
  return {
    status: status.status === 'aborted' ? 'aborted' : terminal ? 'done' : 'running',
    totalChunks: status.totalChunks,
    completedChunks: status.completedChunks,
    failedChunks: status.failedChunks,
    cacheHits: status.cacheHits,
    totalLines: status.totalLines,
    translatedLineCount: status.translatedLines,
    errors: error,
    fatal: status.status === 'failed' ? status.errorSummary : undefined,
    videoKey: status.videoKey,
    provider: status.provider,
  }
}

export default function App() {
  const [daemon, setDaemon] = useState<DaemonStatus>({ state: 'checking' })
  const [providers, setProviders] = useState<ProviderInfo[]>([])
  const [providerLoadError, setProviderLoadError] = useState<string | null>(null)
  const [chosen, setChosen] = useState<string>('')
  const [liveMode, setLiveMode] = useState(false)

  const [overlayStatus, setOverlayStatus] = useState<OverlayStatus | null>(null)
  const [translateStatus, setTranslateStatus] = useState<TranslateStatus | null>(null)

  const [extract, setExtract] = useState<ExtractState>({ state: 'idle' })
  const [job, setJob] = useState<TranslateJob | null>(null)
  const [recentJobs, setRecentJobs] = useState<JobSummary[]>([])
  const [pasteText, setPasteText] = useState('')
  const [pasteResult, setPasteResult] = useState<{ ok: boolean; message: string } | null>(null)
  const [installDir, setInstallDir] = useState<string | null>(null)
  const startCommand = useMemo(() => buildStartCommand(installDir), [installDir])
  const [copiedStart, setCopiedStart] = useState(false)

  const refreshRecentJobs = async () => {
    setRecentJobs(await client.listJobs(5))
  }

  const refreshTranslateStatus = async () => {
    try {
      const status = await sendToActiveTab<TranslateStatus>({
        type: 'GET_TRANSLATE_STATUS',
      } satisfies ContentMessage)
      setTranslateStatus(status)
      const restored = jobFromTranslateStatus(status)
      if (restored) setJob(restored)
    } catch {
      setTranslateStatus(null)
    }
  }

  const refreshOverlayStatus = async () => {
    try {
      const status = await sendToActiveTab<OverlayStatus>({
        type: 'PING_OVERLAY',
      } satisfies ContentMessage)
      if (status?.ok) {
        setOverlayStatus(status)
        setLiveMode(status.liveMode)
      }
    } catch {
      setOverlayStatus(null)
    }
  }

  const refreshDaemon = async () => {
    setDaemon({ state: 'checking' })
    try {
      const h = await client.health()
      setDaemon({ state: 'connected', serverTime: h.time })
      if (h.install_dir) {
        setInstallDir(h.install_dir)
        void chrome.storage.local.set({ daemonInstallDir: h.install_dir })
      }
      try {
        const list = await client.listProviders()
        setProviders(list)
        setProviderLoadError(null)
        if (list.length > 0 && !chosen) setChosen(list[0].name)
      } catch (err) {
        setProviderLoadError((err as Error).message)
      }
      await refreshRecentJobs().catch(() => {})
    } catch (err) {
      setDaemon({ state: 'offline', error: (err as Error).message })
    }
  }

  useEffect(() => {
    chrome.storage.local.get('daemonInstallDir').then((r) => {
      const dir = r.daemonInstallDir
      if (typeof dir === 'string' && dir) setInstallDir(dir)
    })
  }, [])

  useEffect(() => {
    void refreshDaemon()
    void refreshOverlayStatus()
    void refreshTranslateStatus()
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [])

  useEffect(() => {
    const id = window.setInterval(() => {
      if (daemon.state === 'connected') void refreshRecentJobs().catch(() => {})
      void refreshOverlayStatus()
      void refreshTranslateStatus()
    }, 2500)
    return () => window.clearInterval(id)
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [daemon.state])

  const onCopyStartCommand = async () => {
    try {
      await navigator.clipboard.writeText(startCommand)
      setCopiedStart(true)
      setTimeout(() => setCopiedStart(false), 1500)
    } catch (err) {
      console.error('[DualSub] clipboard write failed:', err)
    }
  }

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

  const onApplyPasted = async () => {
    if (!pasteText.trim()) return
    const parsed = parseTranslatedTranscript(pasteText)
    if (parsed.length === 0) {
      setPasteResult({ ok: false, message: 'No [index] lines found. Use [1] text format.' })
      return
    }
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

  const onTranslate = async () => {
    if (!chosen) return
    setJob({
      status: 'extracting',
      totalChunks: 0,
      completedChunks: 0,
      failedChunks: 0,
      cacheHits: 0,
      totalLines: 0,
      translatedLineCount: 0,
      errors: [],
      provider: chosen,
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

    try {
      await sendToActiveTab<SimpleAck>({
        type: 'SET_OVERLAY',
        videoKey: payload.videoKey,
        translations: {},
        autoTranslate: {
          provider: chosen,
          sourceLang: PREFERRED_SOURCE,
          targetLang: TARGET_LANG,
        },
      } satisfies ContentMessage)
    } catch {
      // Overlay is best-effort; still try to start the translation.
    }

    setJob((j) =>
      j && {
        ...j,
        source: payload,
        videoKey: payload.videoKey,
        status: 'running',
        totalLines: payload.entries.length,
      },
    )

    try {
      await sendToActiveTab<SimpleAck>({
        type: 'START_TRANSLATE',
        payload,
        request: {
          site: payload.site,
          video_key: payload.videoKey,
          title: payload.title,
          provider: chosen,
          source_lang: PREFERRED_SOURCE,
          target_lang: TARGET_LANG,
          lines: payload.entries.map((e) => ({ index: e.index, text: e.originalText })),
        },
      } satisfies ContentMessage)
      await refreshTranslateStatus().catch(() => {})
      await refreshRecentJobs().catch(() => {})
    } catch (err) {
      setJob((j) => j && { ...j, status: 'done', fatal: (err as Error).message })
    }
  }

  const onAbort = () => {
    void sendToActiveTab<SimpleAck>({ type: 'CANCEL_TRANSLATE' } satisfies ContentMessage).catch(() => {})
    setJob((j) => j && { ...j, status: 'aborted' })
  }

  const onToggleOverlay = async () => {
    try {
      await sendToActiveTab<SimpleAck>({
        type: overlayStatus?.mounted ? 'HIDE_OVERLAY' : 'SHOW_OVERLAY',
      } satisfies ContentMessage)
      void refreshOverlayStatus()
    } catch {
      // ignore
    }
  }

  const onClearJobs = async () => {
    await client.clearJobs()
    setRecentJobs([])
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
      void refreshOverlayStatus()
    } catch {
      setLiveMode(!enabled)
    }
  }

  const progressPct = useMemo(() => {
    if (!job) return 0
    if (job.status === 'done') return 100
    if (job.totalChunks > 0) {
      return Math.min(100, Math.round((job.completedChunks / job.totalChunks) * 100))
    }
    if (job.totalLines > 0) {
      return Math.min(100, Math.round((job.translatedLineCount / job.totalLines) * 100))
    }
    return 0
  }, [job])

  const currentPageLabel = compactVideoKey(overlayStatus?.videoKey ?? job?.videoKey)
  const activeJob = job?.status === 'running' || job?.status === 'extracting'
  const recentNeedsAttention = recentJobs.some((j) => j.status === 'running' || j.status === 'failed' || j.status === 'partial')
  const recentDefaultKey = recentNeedsAttention ? ['jobs'] : []

  return (
    <ConfigProvider>
      <div style={{ width: 392, padding: 14, background: '#f6f8fb' }}>
        <div style={{ ...sectionTitleStyle, marginBottom: 12 }}>
          <div>
            <Typography.Title level={5} style={{ margin: 0 }}>
              DualSub Next
            </Typography.Title>
            <Space size={6} style={{ marginTop: 4 }}>
              <StatusTag daemon={daemon} />
              {translateStatus?.active && translateStatus.status === 'running' && (
                <Tag color="processing">Translating</Tag>
              )}
            </Space>
          </div>
          <Space size={4}>
            <Tooltip title="Refresh">
              <Button icon={<ReloadOutlined />} onClick={refreshDaemon} size="small" type="text" />
            </Tooltip>
            <Tooltip title="Settings">
              <Button
                icon={<SettingOutlined />}
                onClick={() => chrome.runtime.openOptionsPage()}
                size="small"
                type="text"
              />
            </Tooltip>
          </Space>
        </div>

        {daemon.state === 'offline' && (
          <div style={{ ...panelStyle, marginBottom: 10 }}>
            <Typography.Text strong>Daemon not reachable</Typography.Text>
            <Typography.Paragraph type="secondary" style={{ fontSize: 12, margin: '4px 0 8px' }}>
              Start the local daemon, then refresh.
            </Typography.Paragraph>
            <Space.Compact style={{ width: '100%' }}>
              <Input
                readOnly
                size="small"
                value={startCommand}
                style={{ fontFamily: 'monospace', fontSize: 11 }}
              />
              <Button size="small" icon={<CopyOutlined />} onClick={onCopyStartCommand} />
            </Space.Compact>
            {!installDir && (
              <Typography.Text type="secondary" style={{ display: 'block', fontSize: 11, marginTop: 4 }}>
                找不到腳本的話，請先 cd 到 dualsub 安裝目錄。
              </Typography.Text>
            )}
            {copiedStart && (
              <Typography.Text type="success" style={{ display: 'block', fontSize: 11, marginTop: 4 }}>
                Copied.
              </Typography.Text>
            )}
          </div>
        )}

        <Space direction="vertical" size={10} style={{ width: '100%' }}>
          <div style={mutedPanelStyle}>
            <div style={sectionTitleStyle}>
              <Typography.Text strong>Current Page</Typography.Text>
              {overlayStatus?.liveMode && <Tag color="blue">Live</Tag>}
            </div>
            <Typography.Text
              style={{
                display: 'block',
                maxWidth: '100%',
                overflow: 'hidden',
                textOverflow: 'ellipsis',
                whiteSpace: 'nowrap',
                fontSize: 12,
              }}
              title={currentPageLabel}
            >
              {currentPageLabel}
            </Typography.Text>
            <Space size={6} wrap style={{ marginTop: 8 }}>
              {overlayStatus === null ? (
                <Tag color="default">No subtitle page</Tag>
              ) : overlayStatus.translationsCount > 0 ? (
                <Tag color="green">Translated {overlayStatus.translationsCount} lines</Tag>
              ) : overlayStatus.mounted ? (
                <Tag color="orange">Overlay active</Tag>
              ) : (
                <Tag color="default">Not translated</Tag>
              )}
            </Space>
          </div>

          <div style={panelStyle}>
            <div style={sectionTitleStyle}>
              <Typography.Text strong>Translate Transcript</Typography.Text>
              {job?.provider && <Typography.Text type="secondary" style={{ fontSize: 12 }}>{job.provider}</Typography.Text>}
            </div>
            <Space.Compact style={{ width: '100%' }}>
              <Select
                value={chosen}
                onChange={setChosen}
                disabled={providers.length === 0 || activeJob}
                style={{ width: 122 }}
                placeholder="Provider"
                options={providers.map((p) => ({ value: p.name, label: p.name }))}
              />
              <Button
                type="primary"
                icon={<TranslationOutlined />}
                onClick={onTranslate}
                disabled={daemon.state !== 'connected' || providers.length === 0 || activeJob}
                block
              >
                Translate
              </Button>
            </Space.Compact>

            {providerLoadError && (
              <Typography.Text type="danger" style={{ display: 'block', fontSize: 12, marginTop: 6 }}>
                providers: {providerLoadError}
              </Typography.Text>
            )}
            {providers.length === 0 && daemon.state === 'connected' && !providerLoadError && (
              <Typography.Text type="warning" style={{ display: 'block', fontSize: 12, marginTop: 6 }}>
                No providers configured. Open Settings to set up.
              </Typography.Text>
            )}

            {job && (
              <div style={{ marginTop: 10 }}>
                {job.status === 'extracting' ? (
                  <Typography.Text type="secondary" style={{ fontSize: 12 }}>
                    Extracting transcript from page...
                  </Typography.Text>
                ) : (
                  <>
                    <div style={{ display: 'flex', justifyContent: 'space-between', gap: 8, fontSize: 12 }}>
                      <Typography.Text type="secondary">
                        {job.totalChunks > 0
                          ? `${job.completedChunks}/${job.totalChunks} chunks`
                          : 'Preparing chunks'}
                        {job.failedChunks > 0 ? ` · ${job.failedChunks} failed` : ''}
                        {job.cacheHits > 0 ? ` · ${job.cacheHits} cached` : ''}
                      </Typography.Text>
                      <Typography.Text type="secondary">
                        {job.translatedLineCount}/{job.totalLines || 0} lines
                      </Typography.Text>
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
                  <Alert
                    type="error"
                    showIcon
                    message="Translate failed"
                    description={job.fatal}
                    style={{ marginTop: 8 }}
                  />
                )}
              </div>
            )}
          </div>

          <div style={panelStyle}>
            <div style={sectionTitleStyle}>
              <Typography.Text strong>Quick Actions</Typography.Text>
            </div>
            <Space direction="vertical" size={8} style={{ width: '100%' }}>
              <Space style={{ width: '100%', justifyContent: 'space-between' }}>
                <Typography.Text>Live mode</Typography.Text>
                <Switch
                  checked={liveMode}
                  onChange={onLiveModeToggle}
                  disabled={daemon.state !== 'connected' || providers.length === 0}
                />
              </Space>
              <Space.Compact style={{ width: '100%' }}>
                <Button
                  icon={overlayStatus?.mounted ? <EyeInvisibleOutlined /> : <EyeOutlined />}
                  onClick={onToggleOverlay}
                  disabled={overlayStatus === null}
                  block
                >
                  {overlayStatus?.mounted ? 'Hide Overlay' : 'Show Overlay'}
                </Button>
                <Button
                  icon={<CopyOutlined />}
                  onClick={onExtract}
                  loading={extract.state === 'working'}
                  block
                >
                  Extract & Copy
                </Button>
              </Space.Compact>
              {extract.state === 'copied' && (
                <Alert type="success" showIcon message={`Copied ${extract.entries} lines`} />
              )}
              {extract.state === 'error' && (
                <Alert type="error" showIcon message={extract.code} description={extract.message} />
              )}
            </Space>
          </div>

          <div style={panelStyle}>
            <div style={sectionTitleStyle}>
              <Typography.Text strong>Recent Jobs</Typography.Text>
              <Popconfirm
                title="Clear recent job history?"
                description="Translations and transcript cache will be kept."
                okText="Clear"
                cancelText="Cancel"
                onConfirm={onClearJobs}
                disabled={recentJobs.length === 0}
              >
                <Button
                  size="small"
                  type="text"
                  danger
                  icon={<DeleteOutlined />}
                  disabled={recentJobs.length === 0}
                >
                  Clear
                </Button>
              </Popconfirm>
            </div>
            {recentJobs.length === 0 ? (
              <Typography.Text type="secondary" style={{ fontSize: 12 }}>
                No recent jobs.
              </Typography.Text>
            ) : (
              <Collapse
                size="small"
                ghost
                defaultActiveKey={recentDefaultKey}
                items={[
                  {
                    key: 'jobs',
                    label: `${recentJobs.length} job${recentJobs.length === 1 ? '' : 's'}`,
                    children: recentJobs.map((j) => (
                      <div key={j.job_id} style={{ fontSize: 11, marginBottom: 6 }}>
                        <Tag color={statusColor(j.status)}>{j.status}</Tag>
                        <Typography.Text
                          style={{
                            maxWidth: 210,
                            display: 'inline-block',
                            overflow: 'hidden',
                            textOverflow: 'ellipsis',
                            whiteSpace: 'nowrap',
                            verticalAlign: 'middle',
                          }}
                          title={j.video_key}
                        >
                          {compactVideoKey(j.video_key)}
                        </Typography.Text>
                        <Typography.Text type="secondary"> · {j.completed_chunks}/{j.total_chunks}</Typography.Text>
                        {j.error_summary ? (
                          <Typography.Text type="danger"> · {j.error_summary}</Typography.Text>
                        ) : null}
                      </div>
                    )),
                  },
                ]}
              />
            )}
          </div>

          <Collapse
            size="small"
            items={[
              {
                key: 'manual',
                label: 'Manual Paste Fallback',
                children: (
                  <Space direction="vertical" size="small" style={{ width: '100%' }}>
                    <Typography.Text type="secondary" style={{ fontSize: 12 }}>
                      Paste translated result here. Keep `[index] text` format.
                    </Typography.Text>
                    <Input.TextArea
                      rows={4}
                      value={pasteText}
                      onChange={(e) => { setPasteText(e.target.value); setPasteResult(null) }}
                      placeholder={'[1] 翻譯後的文字\n[2] 第二行翻譯\n...'}
                      style={{ fontSize: 12 }}
                    />
                    <Button
                      icon={<CheckOutlined />}
                      onClick={onApplyPasted}
                      disabled={!pasteText.trim()}
                      block
                    >
                      Apply to overlay
                    </Button>
                    {pasteResult && (
                      <Alert
                        type={pasteResult.ok ? 'success' : 'error'}
                        showIcon
                        message={pasteResult.message}
                      />
                    )}
                  </Space>
                ),
              },
            ]}
          />
        </Space>
      </div>
    </ConfigProvider>
  )
}

function StatusTag({ daemon }: { daemon: DaemonStatus }) {
  switch (daemon.state) {
    case 'connected':
      return <Tag color="green">Connected</Tag>
    case 'offline':
      return <Tag color="red">Offline</Tag>
    case 'checking':
      return <Tag color="blue">Checking</Tag>
  }
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
