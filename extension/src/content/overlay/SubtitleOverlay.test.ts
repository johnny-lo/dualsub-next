import { afterEach, beforeEach, describe, expect, it } from 'vitest'
import { SubtitleOverlay } from './SubtitleOverlay'

type StorageRecord = Record<string, unknown>

/**
 * Minimal synchronous chrome.storage.local stub. Synchronous callbacks keep the
 * constructor's load path deterministic, which is what the reveal state needs.
 */
function installChromeStub(initial: StorageRecord = {}): StorageRecord {
  const store: StorageRecord = { ...initial }
  ;(globalThis as unknown as { chrome: unknown }).chrome = {
    storage: {
      local: {
        get: (keys: string[], cb: (data: StorageRecord) => void) => {
          const out: StorageRecord = {}
          for (const key of keys) if (key in store) out[key] = store[key]
          cb(out)
        },
        set: (obj: StorageRecord, cb?: () => void) => {
          Object.assign(store, obj)
          cb?.()
        },
        remove: (key: string, cb?: () => void) => {
          delete store[key]
          cb?.()
        },
      },
    },
  }
  return store
}

function shadow(): ShadowRoot {
  const host = document.getElementById('dualsub-overlay-host')
  if (!host?.shadowRoot) throw new Error('overlay host is not mounted')
  return host.shadowRoot
}

const container = () => shadow().querySelector('.container') as HTMLElement
const originalText = () => shadow().querySelector('.original')?.textContent ?? null
const translatedText = () => shadow().querySelector('.translated')?.textContent ?? null

function click(): void {
  container().dispatchEvent(
    new MouseEvent('mousedown', { clientX: 100, clientY: 100, bubbles: true }),
  )
  window.dispatchEvent(new MouseEvent('mouseup', { clientX: 101, clientY: 100, bubbles: true }))
}

function drag(): void {
  container().dispatchEvent(
    new MouseEvent('mousedown', { clientX: 100, clientY: 100, bubbles: true }),
  )
  window.dispatchEvent(new MouseEvent('mousemove', { clientX: 160, clientY: 180, bubbles: true }))
  window.dispatchEvent(new MouseEvent('mouseup', { clientX: 160, clientY: 180, bubbles: true }))
}

describe('SubtitleOverlay reveal toggle', () => {
  let overlay: SubtitleOverlay | null = null

  const mount = (stored: StorageRecord = {}): SubtitleOverlay => {
    installChromeStub(stored)
    overlay = new SubtitleOverlay()
    overlay.setTranslations({ hello: '你好' })
    overlay.render(['hello'])
    return overlay
  }

  beforeEach(() => {
    document.body.replaceChildren()
    document.head.replaceChildren()
  })

  afterEach(() => {
    overlay?.destroy()
    overlay = null
  })

  it('renders the original but hides the translation by default', () => {
    mount()
    expect(originalText()).toBe('hello')
    expect(translatedText()).toBe('')
  })

  it('reveals the current cue on click, without waiting for the next cue', () => {
    mount()
    click()
    expect(translatedText()).toBe('你好')
    expect(originalText()).toBe('hello')
  })

  it('hides the translation again on a second click', () => {
    mount()
    click()
    click()
    expect(translatedText()).toBe('')
  })

  it('keeps the revealed state for cues that arrive later', () => {
    const o = mount()
    click()
    o.patchTranslations({ world: '世界' })
    o.render(['world'])
    expect(translatedText()).toBe('世界')
  })

  it('does not toggle when the overlay is dragged to reposition it', () => {
    mount()
    drag()
    expect(translatedText()).toBe('')
  })

  it('persists the revealed choice', () => {
    const store = installChromeStub()
    overlay = new SubtitleOverlay()
    overlay.setTranslations({ hello: '你好' })
    overlay.render(['hello'])
    click()
    expect(store.dualsubOverlayRevealed).toBe(true)
    click()
    expect(store.dualsubOverlayRevealed).toBe(false)
  })

  it('starts revealed when storage says so', () => {
    mount({ dualsubOverlayRevealed: true })
    expect(translatedText()).toBe('你好')
  })
})
