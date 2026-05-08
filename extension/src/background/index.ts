// Background service worker.
// Periodically polls the daemon's /healthz and renders the toolbar icon to
// reflect connection state — a small green dot in the bottom-right corner
// when the daemon is reachable, no dot when it is offline.

import { DAEMON_URL } from '@/shared/DaemonClient'

const HEALTH_ALARM = 'daemon-healthcheck'
const POLL_INTERVAL_MIN = 0.5 // 30 seconds — Chrome MV3 minimum is 0.5
const HEALTH_TIMEOUT_MS = 2000
const ICON_SIZE = 32

function drawIcon(connected: boolean): ImageData {
  const canvas = new OffscreenCanvas(ICON_SIZE, ICON_SIZE)
  const ctx = canvas.getContext('2d')
  if (!ctx) throw new Error('OffscreenCanvas 2d context unavailable')

  ctx.clearRect(0, 0, ICON_SIZE, ICON_SIZE)

  // Rounded-square base in dark gray.
  ctx.fillStyle = '#262626'
  ctx.beginPath()
  ctx.roundRect(2, 2, ICON_SIZE - 4, ICON_SIZE - 4, 6)
  ctx.fill()

  // "D" wordmark.
  ctx.fillStyle = '#ffffff'
  ctx.font = 'bold 18px sans-serif'
  ctx.textAlign = 'center'
  ctx.textBaseline = 'middle'
  ctx.fillText('D', ICON_SIZE / 2, ICON_SIZE / 2 + 1)

  // Connection indicator: small green circle, bottom-right.
  if (connected) {
    ctx.fillStyle = '#52c41a'
    ctx.strokeStyle = '#ffffff'
    ctx.lineWidth = 1.5
    ctx.beginPath()
    ctx.arc(ICON_SIZE - 7, ICON_SIZE - 7, 5, 0, Math.PI * 2)
    ctx.fill()
    ctx.stroke()
  }

  return ctx.getImageData(0, 0, ICON_SIZE, ICON_SIZE)
}

async function applyConnectionState(connected: boolean): Promise<void> {
  await chrome.action.setIcon({ imageData: drawIcon(connected) })
  // Clear any leftover badge from a previous version.
  await chrome.action.setBadgeText({ text: '' })
  await chrome.action.setTitle({
    title: connected
      ? 'DualSub Next — daemon connected'
      : 'DualSub Next — daemon offline',
  })
}

async function checkHealth(): Promise<void> {
  let ok = false
  try {
    const res = await fetch(`${DAEMON_URL}/healthz`, {
      signal: AbortSignal.timeout(HEALTH_TIMEOUT_MS),
    })
    ok = res.ok
  } catch {
    ok = false
  }
  await applyConnectionState(ok)
}

function ensureAlarm(): void {
  chrome.alarms.create(HEALTH_ALARM, { periodInMinutes: POLL_INTERVAL_MIN })
}

chrome.runtime.onInstalled.addListener((details) => {
  console.log('[DualSub] installed:', details.reason)
  ensureAlarm()
  void checkHealth()
})

chrome.runtime.onStartup.addListener(() => {
  console.log('[DualSub] startup')
  ensureAlarm()
  void checkHealth()
})

chrome.alarms.onAlarm.addListener((alarm) => {
  if (alarm.name === HEALTH_ALARM) void checkHealth()
})

// SW may be respawned without onInstalled/onStartup firing again — kick a check
// at module load so the icon is correct immediately after revival.
void checkHealth()
ensureAlarm()
