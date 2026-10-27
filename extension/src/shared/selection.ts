// The remembered provider/model/effort choice. The popup edits it; the content
// script reads it for auto-translate and live mode, so both always follow the
// user's current choice rather than a snapshot taken when they started.

import type { ModelInfo } from './DaemonClient'

export const SELECTION_KEY = 'dualsubSelection'

/** '' means "let the CLI / daemon config decide". */
export interface ModelChoice {
  model: string
  effort: string
}

export interface Selection {
  provider: string
  /** Each provider remembers its own last model and effort. */
  perProvider: Record<string, ModelChoice>
}

export const CLI_DEFAULT: ModelChoice = Object.freeze({ model: '', effort: '' })

function isChoice(v: unknown): v is ModelChoice {
  if (!v || typeof v !== 'object') return false
  const c = v as Record<string, unknown>
  return typeof c.model === 'string' && typeof c.effort === 'string'
}

export function parseSelection(raw: unknown): Selection | null {
  if (!raw || typeof raw !== 'object') return null
  const r = raw as Record<string, unknown>
  if (typeof r.provider !== 'string') return null
  const perProvider: Record<string, ModelChoice> = {}
  if (r.perProvider && typeof r.perProvider === 'object') {
    for (const [name, choice] of Object.entries(r.perProvider as Record<string, unknown>)) {
      if (isChoice(choice)) perProvider[name] = { model: choice.model, effort: choice.effort }
    }
  }
  return { provider: r.provider, perProvider }
}

export function choiceFor(sel: Selection | null, provider: string): ModelChoice {
  return sel?.perProvider[provider] ?? CLI_DEFAULT
}

export function withProvider(sel: Selection | null, provider: string): Selection {
  return { provider, perProvider: { ...(sel?.perProvider ?? {}) } }
}

export function withChoice(sel: Selection | null, provider: string, choice: ModelChoice): Selection {
  return {
    provider: sel?.provider ?? provider,
    perProvider: { ...(sel?.perProvider ?? {}), [provider]: { model: choice.model, effort: choice.effort } },
  }
}

/** Keeps the remembered provider if it is still offered, else the first one. */
export function reconcileProvider(sel: Selection | null, offered: string[]): Selection | null {
  if (offered.length === 0) return null
  if (sel && offered.includes(sel.provider)) return sel
  return withProvider(sel, offered[0])
}

export function defaultChoiceFor(model: ModelInfo): ModelChoice {
  return { model: model.id, effort: model.default_effort ?? '' }
}

/**
 * Fits a remembered choice to a freshly fetched model list. Call it only with
 * a list that was fetched successfully: a failed fetch must never clear the
 * remembered model.
 */
export function reconcileChoice(choice: ModelChoice, models: ModelInfo[]): ModelChoice {
  if (!choice.model) return CLI_DEFAULT
  const info = models.find((m) => m.id === choice.model)
  if (!info) return CLI_DEFAULT
  const efforts = info.efforts ?? []
  if (efforts.length === 0) return { model: info.id, effort: '' }
  if (efforts.includes(choice.effort)) return choice
  return defaultChoiceFor(info)
}

/**
 * The provider/model/effort fields of a translate request. `fallbackProvider`
 * covers installs that predate the remembered selection.
 */
export function requestTarget(
  sel: Selection | null,
  fallbackProvider: string,
): { provider: string; model?: string; effort?: string } {
  const provider = sel?.provider || fallbackProvider
  const { model, effort } = choiceFor(sel, provider)
  return {
    provider,
    ...(model ? { model } : {}),
    ...(model && effort ? { effort } : {}),
  }
}

export async function loadSelection(): Promise<Selection | null> {
  try {
    const data = await chrome.storage.local.get(SELECTION_KEY)
    return parseSelection(data[SELECTION_KEY])
  } catch {
    return null
  }
}

export async function saveSelection(sel: Selection): Promise<void> {
  await chrome.storage.local.set({ [SELECTION_KEY]: sel })
}
