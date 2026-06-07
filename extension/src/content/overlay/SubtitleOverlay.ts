import { normalizeText } from '@/shared/transcript'

export interface OverlayStyle {
  originalSize?: number
  originalColor?: string
  translatedSize?: number
  translatedColor?: string
}

interface OverlayPosition {
  x: number
  y: number
}

const DEFAULT_STYLE: Required<OverlayStyle> = {
  originalSize: 18,
  originalColor: '#ffffff',
  translatedSize: 18,
  translatedColor: '#ffd54f',
}

const DEFAULT_POSITION: OverlayPosition = { x: 0.5, y: 0.84 }
const POSITION_STORAGE_KEY = 'dualsubOverlayPosition'

function clamp(value: number, min: number, max: number): number {
  return Math.min(max, Math.max(min, value))
}

/**
 * Bilingual subtitle overlay rendered into a Shadow DOM so the host page's
 * CSS cannot affect it. Position is stored as normalized host coordinates so
 * dragging remains stable across normal, fullscreen, and resized viewports.
 */
export class SubtitleOverlay {
  private host: HTMLDivElement
  private root: ShadowRoot
  private container: HTMLDivElement
  private linesEl: HTMLDivElement
  private styleEl: HTMLStyleElement
  private translations = new Map<string, string>()
  private dragging = false
  private dragOffsetFromCenter = { x: 0, y: 0 }
  private position: OverlayPosition = { ...DEFAULT_POSITION }
  private readonly onFullscreenChange = () => {
    this.mountHost()
    window.requestAnimationFrame(() => this.applyPosition())
  }
  private readonly onResize = () => this.applyPosition()
  private nativeCaptionStyle: HTMLStyleElement | null = null
  private currentStyle: Required<OverlayStyle> = { ...DEFAULT_STYLE }

  private onMouseDown = (e: MouseEvent) => {
    this.dragging = true
    const rect = this.container.getBoundingClientRect()
    this.dragOffsetFromCenter = {
      x: e.clientX - (rect.left + rect.width / 2),
      y: e.clientY - (rect.top + rect.height / 2),
    }
    e.preventDefault()
  }
  private onMouseMove = (e: MouseEvent) => {
    if (!this.dragging) return
    const bounds = this.getHostBounds()
    if (bounds.width <= 0 || bounds.height <= 0) return

    this.position = {
      x: clamp(
        (e.clientX - this.dragOffsetFromCenter.x - bounds.left) / bounds.width,
        0,
        1,
      ),
      y: clamp(
        (e.clientY - this.dragOffsetFromCenter.y - bounds.top) / bounds.height,
        0,
        1,
      ),
    }
    this.applyPosition()
  }
  private onMouseUp = () => {
    if (!this.dragging) return
    this.dragging = false
    this.savePosition()
  }

  constructor() {
    this.host = document.createElement('div')
    this.host.id = 'dualsub-overlay-host'
    this.host.style.cssText =
      'all: initial; position: fixed !important; inset: 0 !important; z-index: 2147483647 !important; pointer-events: none !important;'
    this.root = this.host.attachShadow({ mode: 'open' })

    this.styleEl = document.createElement('style')
    this.root.appendChild(this.styleEl)
    this.applyStyle(this.currentStyle)

    this.container = document.createElement('div')
    this.container.className = 'container empty'
    this.linesEl = document.createElement('div')
    this.linesEl.className = 'lines'
    this.container.appendChild(this.linesEl)
    this.root.appendChild(this.container)

    this.attachDragHandlers()
    this.mountHost()
    this.hideNativeCaptions()
    document.addEventListener('fullscreenchange', this.onFullscreenChange)
    window.addEventListener('resize', this.onResize)

    // Load saved appearance and normalized position from chrome.storage.
    this.loadStyleFromStorage()
    this.loadPositionFromStorage()
  }

  private buildCSS(s: Required<OverlayStyle>): string {
    return `
      :host { all: initial; }
      .container {
        position: absolute;
        z-index: 2147483647;
        transform: translate(-50%, -50%);
        max-width: 80%;
        padding: 10px 18px;
        background: rgba(0, 0, 0, 0.78);
        color: #fff;
        font-family: -apple-system, BlinkMacSystemFont, "Segoe UI", Roboto, sans-serif;
        text-align: center;
        border-radius: 6px;
        pointer-events: auto;
        cursor: move;
        user-select: none;
        line-height: 1.35;
        box-shadow: 0 2px 12px rgba(0,0,0,0.4);
      }
      .container.empty { display: none; }
      .line + .line { margin-top: 6px; }
      .original {
        font-size: ${s.originalSize}px;
        color: ${s.originalColor};
      }
      .translated {
        font-size: ${s.translatedSize}px;
        color: ${s.translatedColor};
        margin-top: 4px;
      }
      .translated:empty { display: none; }
      .original:empty { display: none; }
    `
  }

  private applyStyle(s: Required<OverlayStyle>) {
    this.currentStyle = s
    this.styleEl.textContent = this.buildCSS(s)
  }

  private loadStyleFromStorage() {
    try {
      chrome.storage.local.get(['dualsubOverlayStyle'], (data) => {
        if (data.dualsubOverlayStyle) {
          this.applyStyle({ ...DEFAULT_STYLE, ...data.dualsubOverlayStyle })
        }
      })
    } catch {
      // storage not available (e.g. in tests)
    }
  }

  setStyle(style: OverlayStyle): void {
    this.applyStyle({ ...DEFAULT_STYLE, ...style })
    window.requestAnimationFrame(() => this.applyPosition())
  }

  private loadPositionFromStorage() {
    try {
      chrome.storage.local.get([POSITION_STORAGE_KEY], (data) => {
        const stored = data[POSITION_STORAGE_KEY] as Partial<OverlayPosition> | undefined
        if (
          stored &&
          typeof stored.x === 'number' &&
          Number.isFinite(stored.x) &&
          typeof stored.y === 'number' &&
          Number.isFinite(stored.y)
        ) {
          this.position = {
            x: clamp(stored.x, 0, 1),
            y: clamp(stored.y, 0, 1),
          }
        }
        this.applyPosition()
      })
    } catch {
      this.applyPosition()
    }
  }

  private savePosition() {
    try {
      void chrome.storage.local.set({ [POSITION_STORAGE_KEY]: this.position })
    } catch {
      // storage not available (e.g. in tests)
    }
  }

  private getHostBounds(): DOMRect {
    const bounds = this.host.getBoundingClientRect()
    if (bounds.width > 0 && bounds.height > 0) return bounds
    return new DOMRect(0, 0, window.innerWidth, window.innerHeight)
  }

  private displayPosition(): OverlayPosition {
    const bounds = this.getHostBounds()
    const rect = this.container.getBoundingClientRect()
    if (bounds.width <= 0 || bounds.height <= 0) return this.position

    const halfWidth = Math.min(rect.width / 2, bounds.width / 2)
    const halfHeight = Math.min(rect.height / 2, bounds.height / 2)
    const minX = halfWidth / bounds.width
    const maxX = 1 - minX
    const minY = halfHeight / bounds.height
    const maxY = 1 - minY

    return {
      x: clamp(this.position.x, minX, maxX),
      y: clamp(this.position.y, minY, maxY),
    }
  }

  private applyPosition() {
    const display = this.displayPosition()
    this.container.style.left = `${display.x * 100}%`
    this.container.style.top = `${display.y * 100}%`
    this.container.style.bottom = 'auto'
    this.container.style.transform = 'translate(-50%, -50%)'
  }

  private hideNativeCaptions() {
    if (this.nativeCaptionStyle) return
    this.nativeCaptionStyle = document.createElement('style')
    this.nativeCaptionStyle.id = 'dualsub-hide-native-captions'
    // Hide every known native caption surface across Udemy versions + Netflix.
    // Use opacity:0 + visibility:hidden + color:transparent for defense in
    // depth — host CSS sometimes wins over a single property. textContent
    // reads work regardless of visibility/opacity, so the cue extractor still
    // sees the text. The fallback selector path in UdemyExtractor checks for
    // this style element's id and bypasses its visibility filter accordingly.
    this.nativeCaptionStyle.textContent = `
      [class*="captions-display--"],
      [class*="captions-cue--"],
      [class*="well-module--text--"],
      [class*="well-module--container--"],
      [class*="well--text--"],
      [class*="well--container--"],
      [data-purpose="captions-cue-text"],
      [data-purpose="captions-display"],
      .player-timedtext,
      .player-timedtext-text-container {
        opacity: 0 !important;
        visibility: hidden !important;
        color: transparent !important;
        text-shadow: none !important;
        pointer-events: none !important;
      }
      video::cue {
        opacity: 0 !important;
        color: transparent !important;
        background-color: transparent !important;
        text-shadow: none !important;
      }
    `
    document.head.appendChild(this.nativeCaptionStyle)
  }

  private showNativeCaptions() {
    this.nativeCaptionStyle?.remove()
    this.nativeCaptionStyle = null
  }

  private mountHost() {
    const parent = document.fullscreenElement ?? document.body ?? document.documentElement
    if (this.host.parentElement !== parent) parent.appendChild(this.host)
    this.applyPosition()
  }

  private attachDragHandlers() {
    this.container.addEventListener('mousedown', this.onMouseDown)
    window.addEventListener('mousemove', this.onMouseMove)
    window.addEventListener('mouseup', this.onMouseUp)
  }

  setTranslations(map: Record<string, string>): void {
    this.translations = new Map<string, string>()
    for (const [original, translated] of Object.entries(map)) {
      this.translations.set(normalizeText(original), translated)
    }
  }

  /**
   * Patch in additional translations without resetting the existing map.
   * Used by Live mode when the daemon returns a per-cue translation.
   */
  patchTranslations(map: Record<string, string>): void {
    for (const [original, translated] of Object.entries(map)) {
      this.translations.set(normalizeText(original), translated)
    }
  }

  /** Returns true if lookupTranslation would resolve a non-empty value. */
  hasTranslation(originalText: string): boolean {
    return this.lookupTranslation(originalText) !== null
  }

  get translationsCount(): number {
    return this.translations.size
  }

  /**
   * Resolve the rendered text → cached translation. Tries an exact normalized
   * match first; on miss, falls back to (a) longest-substring containment and
   * (b) word-overlap Jaccard. Bridges Udemy VTT-vs-DOM divergence and
   * cross-track key drift (e.g. [CC] vs [自動]).
   */
  private lookupTranslation(originalText: string): string | null {
    const normalized = normalizeText(originalText)
    if (!normalized) return null

    const exact = this.translations.get(normalized)
    if (exact) return exact

    let best: { value: string; score: number } | null = null

    // Pass 1: substring containment, score = matched length.
    for (const [key, value] of this.translations) {
      if (key.length < 4) continue
      const shorter = Math.min(normalized.length, key.length)
      const longer = Math.max(normalized.length, key.length)
      if (longer === 0 || shorter / longer < 0.3) continue
      if (normalized.includes(key) || key.includes(normalized)) {
        if (!best || shorter > best.score) best = { value, score: shorter }
      }
    }
    if (best) return best.value

    // Pass 2: word-overlap Jaccard for cases where punctuation/word-order differs.
    const words = normalized.split(' ').filter((w) => w.length >= 2)
    if (words.length < 2) return null
    const wordSet = new Set(words)
    let bestJ: { value: string; score: number } | null = null
    for (const [key, value] of this.translations) {
      const kWords = key.split(' ').filter((w) => w.length >= 2)
      if (kWords.length < 2) continue
      const kSet = new Set(kWords)
      let inter = 0
      for (const w of wordSet) if (kSet.has(w)) inter++
      const union = wordSet.size + kSet.size - inter
      const j = union === 0 ? 0 : inter / union
      if (j >= 0.6 && (!bestJ || j > bestJ.score)) bestJ = { value, score: j }
    }
    return bestJ?.value ?? null
  }

  /** Debug-only: snapshot the translation map keys (first N). */
  debugSampleKeys(n = 8): string[] {
    return Array.from(this.translations.keys()).slice(0, n)
  }

  render(originalTexts: string[] | null): void {
    this.linesEl.replaceChildren()
    if (!originalTexts || originalTexts.length === 0) {
      this.container.classList.add('empty')
      return
    }
    this.container.classList.remove('empty')
    for (const text of originalTexts) {
      const line = document.createElement('div')
      line.className = 'line'
      const orig = document.createElement('div')
      orig.className = 'original'
      orig.textContent = text
      const trans = document.createElement('div')
      trans.className = 'translated'
      trans.textContent = this.lookupTranslation(text) ?? ''
      line.appendChild(orig)
      line.appendChild(trans)
      this.linesEl.appendChild(line)
    }
    window.requestAnimationFrame(() => this.applyPosition())
  }

  destroy(): void {
    this.container.removeEventListener('mousedown', this.onMouseDown)
    window.removeEventListener('mousemove', this.onMouseMove)
    window.removeEventListener('mouseup', this.onMouseUp)
    window.removeEventListener('resize', this.onResize)
    document.removeEventListener('fullscreenchange', this.onFullscreenChange)
    this.showNativeCaptions()
    this.host.remove()
    this.translations.clear()
  }
}
