// Background service worker.
// Periodically polls the daemon's /healthz and swaps the toolbar icon between
// connected and disconnected variants.

import { DAEMON_URL } from '@/shared/DaemonClient'

const HEALTH_ALARM = 'daemon-healthcheck'
const POLL_INTERVAL_MIN = 0.5 // 30 seconds; Chrome MV3 minimum is 0.5
const HEALTH_TIMEOUT_MS = 2000

const ICONS = {
  connected: {
    16: 'icons/icon-connected-16.png',
    32: 'icons/icon-connected-32.png',
    48: 'icons/icon-connected-48.png',
    128: 'icons/icon-connected-128.png',
  },
  disconnected: {
    16: 'icons/icon-disconnected-16.png',
    32: 'icons/icon-disconnected-32.png',
    48: 'icons/icon-disconnected-48.png',
    128: 'icons/icon-disconnected-128.png',
  },
} as const

async function applyConnectionState(connected: boolean): Promise<void> {
  await chrome.action.setIcon({
    path: connected ? ICONS.connected : ICONS.disconnected,
  })
  await chrome.action.setBadgeText({ text: '' })
  await chrome.action.setTitle({
    title: connected
      ? 'DualSub Next - daemon connected'
      : 'DualSub Next - daemon offline',
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

void checkHealth()
ensureAlarm()
