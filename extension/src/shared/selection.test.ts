import { describe, expect, it } from 'vitest'
import type { ModelInfo } from './DaemonClient'
import {
  CLI_DEFAULT,
  choiceFor,
  defaultChoiceFor,
  parseSelection,
  reconcileChoice,
  reconcileProvider,
  requestTarget,
  withChoice,
  withProvider,
  type Selection,
} from './selection'

const sol: ModelInfo = {
  id: 'gpt-5.6-sol',
  label: 'GPT-5.6-Sol',
  efforts: ['low', 'medium', 'high'],
  default_effort: 'low',
}
const astra: ModelInfo = { id: 'gpt-6-astra', label: 'GPT-6-Astra', efforts: ['low', 'medium'], default_effort: 'medium' }
const flashLow: ModelInfo = { id: 'gemini-3.8-flash-low', label: 'Gemini 3.8 Flash (Low)' }

const codexSol: Selection = {
  provider: 'codex',
  perProvider: { codex: { model: 'gpt-5.6-sol', effort: 'low' } },
}

describe('parseSelection', () => {
  it('rejects non-objects and a missing provider', () => {
    expect(parseSelection(undefined)).toBeNull()
    expect(parseSelection('codex')).toBeNull()
    expect(parseSelection({ perProvider: {} })).toBeNull()
    expect(parseSelection({ provider: 7, perProvider: {} })).toBeNull()
  })

  it('keeps well-formed choices and drops malformed ones', () => {
    const sel = parseSelection({
      provider: 'codex',
      perProvider: {
        codex: { model: 'gpt-5.6-sol', effort: 'low' },
        agy: { model: 3 },
        ollama: 'nope',
      },
    })
    expect(sel).toEqual(codexSol)
  })

  it('treats a missing perProvider as empty', () => {
    expect(parseSelection({ provider: 'agy' })).toEqual({ provider: 'agy', perProvider: {} })
  })
})

describe('choiceFor / withProvider / withChoice', () => {
  it('returns CLI_DEFAULT for an unknown provider or no selection', () => {
    expect(choiceFor(codexSol, 'agy')).toEqual(CLI_DEFAULT)
    expect(choiceFor(null, 'codex')).toEqual(CLI_DEFAULT)
    expect(choiceFor(codexSol, 'codex')).toEqual({ model: 'gpt-5.6-sol', effort: 'low' })
  })

  it("withChoice keeps other providers' choices", () => {
    const next = withChoice(codexSol, 'agy', { model: 'gemini-3.8-flash-low', effort: '' })
    expect(next.provider).toBe('codex')
    expect(next.perProvider.codex).toEqual({ model: 'gpt-5.6-sol', effort: 'low' })
    expect(next.perProvider.agy).toEqual({ model: 'gemini-3.8-flash-low', effort: '' })
  })

  it('withProvider switches provider and keeps every remembered choice', () => {
    const next = withProvider(codexSol, 'agy')
    expect(next).toEqual({ provider: 'agy', perProvider: codexSol.perProvider })
    expect(withProvider(next, 'codex')).toEqual(codexSol)
  })

  it('does not mutate its input', () => {
    const before = JSON.stringify(codexSol)
    withChoice(codexSol, 'codex', CLI_DEFAULT)
    withProvider(codexSol, 'agy')
    expect(JSON.stringify(codexSol)).toBe(before)
  })
})

describe('reconcileProvider', () => {
  it('falls back to the first offered provider and keeps remembered choices', () => {
    const legacy: Selection = { provider: 'gemini', perProvider: codexSol.perProvider }
    expect(reconcileProvider(legacy, ['agy', 'codex'])).toEqual({ provider: 'agy', perProvider: codexSol.perProvider })
  })

  it('keeps an offered provider', () => {
    expect(reconcileProvider(codexSol, ['agy', 'codex'])).toEqual(codexSol)
  })

  it('starts from the first offered provider when nothing is remembered', () => {
    expect(reconcileProvider(null, ['agy', 'codex'])).toEqual({ provider: 'agy', perProvider: {} })
  })

  it('returns null when nothing is offered', () => {
    expect(reconcileProvider(codexSol, [])).toBeNull()
  })
})

describe('reconcileChoice (always given a successfully fetched list)', () => {
  it('model missing from the list → CLI_DEFAULT', () => {
    expect(reconcileChoice({ model: 'gpt-retired', effort: 'low' }, [sol, astra])).toEqual(CLI_DEFAULT)
  })

  it('unsupported effort → the model default effort', () => {
    expect(reconcileChoice({ model: 'gpt-5.6-sol', effort: 'turbo' }, [sol])).toEqual({ model: 'gpt-5.6-sol', effort: 'low' })
  })

  it('empty effort on a model with efforts → the model default effort', () => {
    expect(reconcileChoice({ model: 'gpt-6-astra', effort: '' }, [astra])).toEqual({ model: 'gpt-6-astra', effort: 'medium' })
  })

  it('model without efforts → effort cleared', () => {
    expect(reconcileChoice({ model: 'gemini-3.8-flash-low', effort: 'low' }, [flashLow])).toEqual({
      model: 'gemini-3.8-flash-low',
      effort: '',
    })
  })

  it('a valid choice is kept', () => {
    expect(reconcileChoice({ model: 'gpt-5.6-sol', effort: 'high' }, [sol])).toEqual({ model: 'gpt-5.6-sol', effort: 'high' })
  })

  it('CLI_DEFAULT stays CLI_DEFAULT', () => {
    expect(reconcileChoice(CLI_DEFAULT, [sol])).toEqual(CLI_DEFAULT)
    expect(reconcileChoice({ model: '', effort: 'low' }, [sol])).toEqual(CLI_DEFAULT)
  })
})

describe('defaultChoiceFor', () => {
  it("uses the model's default effort, or '' when it has none", () => {
    expect(defaultChoiceFor(sol)).toEqual({ model: 'gpt-5.6-sol', effort: 'low' })
    expect(defaultChoiceFor(flashLow)).toEqual({ model: 'gemini-3.8-flash-low', effort: '' })
  })
})

describe('requestTarget', () => {
  it('sends the selected model and effort', () => {
    expect(requestTarget(codexSol, 'gemini')).toEqual({ provider: 'codex', model: 'gpt-5.6-sol', effort: 'low' })
  })

  it('omits model and effort for CLI_DEFAULT', () => {
    expect(requestTarget({ provider: 'agy', perProvider: {} }, 'codex')).toEqual({ provider: 'agy' })
  })

  it('falls back to the legacy provider when nothing is selected', () => {
    expect(requestTarget(null, 'gemini')).toEqual({ provider: 'gemini' })
  })
})
