import { useEffect, useState } from 'react'
import {
  Alert,
  Button,
  Card,
  ColorPicker,
  ConfigProvider,
  Divider,
  Form,
  Input,
  InputNumber,
  Space,
  Spin,
  Typography,
} from 'antd'
import { SaveOutlined } from '@ant-design/icons'
import { DaemonClient, type DaemonConfig } from '@/shared/DaemonClient'
import type { OverlayStyle } from '@/content/overlay/SubtitleOverlay'

const client = new DaemonClient()

type SaveState =
  | { state: 'idle' }
  | { state: 'saving' }
  | { state: 'saved'; message: string }
  | { state: 'error'; message: string }

export default function App() {
  const [loading, setLoading] = useState(true)
  const [loadError, setLoadError] = useState<string | null>(null)
  const [config, setConfig] = useState<DaemonConfig | null>(null)
  const [saveState, setSaveState] = useState<SaveState>({ state: 'idle' })
  const [form] = Form.useForm<DaemonConfig>()

  const [overlayStyle, setOverlayStyle] = useState<Required<OverlayStyle>>({
    originalSize: 18,
    originalColor: '#ffffff',
    translatedSize: 18,
    translatedColor: '#ffd54f',
  })
  const [styleSaved, setStyleSaved] = useState(false)

  useEffect(() => {
    void (async () => {
      try {
        const cfg = await client.getConfig()
        setConfig(cfg)
        form.setFieldsValue(cfg)
      } catch (err) {
        setLoadError((err as Error).message)
      } finally {
        setLoading(false)
      }
    })()
    chrome.storage.local.get(['dualsubOverlayStyle'], (data) => {
      if (data.dualsubOverlayStyle) {
        setOverlayStyle((prev) => ({ ...prev, ...data.dualsubOverlayStyle }))
      }
    })
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [])

  const onSaveOverlayStyle = () => {
    chrome.storage.local.set({ dualsubOverlayStyle: overlayStyle }, () => {
      setStyleSaved(true)
      setTimeout(() => setStyleSaved(false), 2000)
    })
  }

  const onSave = async () => {
    setSaveState({ state: 'saving' })
    try {
      const values = await form.validateFields()
      // Merge with existing config so we preserve fields the form does not surface.
      const merged = { ...(config ?? {}), ...values } as DaemonConfig
      await client.putConfig(merged)
      setSaveState({
        state: 'saved',
        message: 'Config saved. Restart the daemon to apply changes.',
      })
    } catch (err) {
      setSaveState({ state: 'error', message: (err as Error).message })
    }
  }

  if (loading) {
    return (
      <ConfigProvider>
        <div style={{ padding: 40, textAlign: 'center' }}>
          <Spin /> Loading config…
        </div>
      </ConfigProvider>
    )
  }

  if (loadError) {
    return (
      <ConfigProvider>
        <div style={{ maxWidth: 720, margin: '40px auto', padding: 24 }}>
          <Alert
            type="error"
            showIcon
            message="Cannot reach daemon"
            description={
              <>
                {loadError}
                <br />
                Run <code>./dualsub serve</code> first, then refresh this page.
              </>
            }
          />
        </div>
      </ConfigProvider>
    )
  }

  return (
    <ConfigProvider>
      <div style={{ maxWidth: 720, margin: '32px auto', padding: 24 }}>
        <Typography.Title level={3} style={{ marginTop: 0 }}>
          DualSub Next — Options
        </Typography.Title>
        <Typography.Paragraph type="secondary">
          These settings are persisted in the daemon's <code>config.toml</code>. You must
          restart <code>dualsub serve</code> for changes to take effect.
        </Typography.Paragraph>

        <Form
          form={form}
          layout="vertical"
          initialValues={config ?? {}}
          onFinish={onSave}
          requiredMark={false}
        >
          <Card size="small" title="Server" style={{ marginBottom: 12 }}>
            <Form.Item label="Listen address" name={['server', 'listen']}>
              <Input placeholder="127.0.0.1:7878" />
            </Form.Item>
          </Card>

          <Card size="small" title="Translate" style={{ marginBottom: 12 }}>
            <Space size="middle" wrap>
              <Form.Item label="Chunk size" name={['translate', 'chunk_size']}>
                <InputNumber min={5} max={200} />
              </Form.Item>
              <Form.Item label="Concurrency" name={['translate', 'concurrency']}>
                <InputNumber min={1} max={10} />
              </Form.Item>
              <Form.Item label="Max attempts" name={['translate', 'max_attempts']}>
                <InputNumber min={1} max={10} />
              </Form.Item>
            </Space>
          </Card>

          <Card size="small" title="Cache" style={{ marginBottom: 12 }}>
            <Form.Item label="SQLite path" name={['cache', 'path']}>
              <Input placeholder="~/.local/share/dualsub/cache.db" />
            </Form.Item>
          </Card>

          <Divider orientation="left" style={{ margin: '16px 0 10px' }}>
            Providers
          </Divider>

          <Card size="small" title="Gemini" style={{ marginBottom: 12 }}>
            <Form.Item label="API key" name={['providers', 'gemini', 'api_key']}>
              <Input.Password placeholder="AIza..." autoComplete="off" />
            </Form.Item>
            <Space size="middle" wrap>
              <Form.Item label="Default model" name={['providers', 'gemini', 'default_model']}>
                <Input placeholder="gemini-2.5-flash" />
              </Form.Item>
              <Form.Item label="Base URL (optional)" name={['providers', 'gemini', 'base_url']}>
                <Input placeholder="(default)" />
              </Form.Item>
            </Space>
          </Card>

          <Card size="small" title="OpenAI" style={{ marginBottom: 12 }}>
            <Form.Item label="API key" name={['providers', 'openai', 'api_key']}>
              <Input.Password placeholder="sk-..." autoComplete="off" />
            </Form.Item>
            <Space size="middle" wrap>
              <Form.Item label="Default model" name={['providers', 'openai', 'default_model']}>
                <Input placeholder="gpt-4o-mini" />
              </Form.Item>
              <Form.Item label="Base URL (optional)" name={['providers', 'openai', 'base_url']}>
                <Input placeholder="(default)" />
              </Form.Item>
            </Space>
          </Card>

          <Card size="small" title="Ollama" style={{ marginBottom: 12 }}>
            <Space size="middle" wrap>
              <Form.Item label="Base URL" name={['providers', 'ollama', 'base_url']}>
                <Input placeholder="http://127.0.0.1:11434" />
              </Form.Item>
              <Form.Item label="Default model" name={['providers', 'ollama', 'default_model']}>
                <Input placeholder="qwen2.5:7b" />
              </Form.Item>
            </Space>
          </Card>

          <Space>
            <Button
              type="primary"
              icon={<SaveOutlined />}
              htmlType="submit"
              loading={saveState.state === 'saving'}
            >
              Save
            </Button>
          </Space>

          {saveState.state === 'saved' && (
            <Alert
              style={{ marginTop: 16 }}
              type="success"
              showIcon
              message={saveState.message}
            />
          )}
          {saveState.state === 'error' && (
            <Alert
              style={{ marginTop: 16 }}
              type="error"
              showIcon
              message="Save failed"
              description={saveState.message}
            />
          )}
        </Form>

        <Divider orientation="left" style={{ margin: '24px 0 16px' }}>
          Subtitle Overlay Style
        </Divider>

        <Card size="small" title="Original text (source language)" style={{ marginBottom: 12 }}>
          <Space size="middle" wrap>
            <div>
              <Typography.Text style={{ fontSize: 12 }}>Font size (px)</Typography.Text>
              <InputNumber
                min={10}
                max={48}
                value={overlayStyle.originalSize}
                onChange={(v) => v && setOverlayStyle((s) => ({ ...s, originalSize: v }))}
                style={{ display: 'block', marginTop: 4 }}
              />
            </div>
            <div>
              <Typography.Text style={{ fontSize: 12 }}>Color</Typography.Text>
              <div style={{ marginTop: 4 }}>
                <ColorPicker
                  value={overlayStyle.originalColor}
                  onChange={(_, hex) => setOverlayStyle((s) => ({ ...s, originalColor: hex }))}
                />
              </div>
            </div>
          </Space>
        </Card>

        <Card size="small" title="Translated text (target language)" style={{ marginBottom: 12 }}>
          <Space size="middle" wrap>
            <div>
              <Typography.Text style={{ fontSize: 12 }}>Font size (px)</Typography.Text>
              <InputNumber
                min={10}
                max={48}
                value={overlayStyle.translatedSize}
                onChange={(v) => v && setOverlayStyle((s) => ({ ...s, translatedSize: v }))}
                style={{ display: 'block', marginTop: 4 }}
              />
            </div>
            <div>
              <Typography.Text style={{ fontSize: 12 }}>Color</Typography.Text>
              <div style={{ marginTop: 4 }}>
                <ColorPicker
                  value={overlayStyle.translatedColor}
                  onChange={(_, hex) => setOverlayStyle((s) => ({ ...s, translatedColor: hex }))}
                />
              </div>
            </div>
          </Space>
        </Card>

        <Space>
          <Button type="primary" icon={<SaveOutlined />} onClick={onSaveOverlayStyle}>
            Save Style
          </Button>
          {styleSaved && (
            <Typography.Text type="success">
              Saved! Reload the video page to apply.
            </Typography.Text>
          )}
        </Space>
      </div>
    </ConfigProvider>
  )
}
