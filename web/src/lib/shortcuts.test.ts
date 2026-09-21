import { describe, expect, it } from 'vitest'

import { looksLikeKeys, toServerRow, validateDraft } from '@/lib/shortcuts'

describe('unified shortcuts', () => {
  it('resolves key combos vs commands from the text', () => {
    for (const t of ['Ctrl-C', 'cmd-c', 'Esc', 'Up', 'Tab', 'Enter']) {
      expect(looksLikeKeys(t)).toBe(true)
      expect(toServerRow({ name: 'x', text: t }).kind).toBe('keys')
    }
    for (const t of ['npm run dev', 'echo hello', 'git status']) {
      expect(looksLikeKeys(t)).toBe(false)
      expect(toServerRow({ name: 'x', text: t }).kind).toBe('cmd')
    }
  })

  it('validates one draft against existing rows', () => {
    const rows = [{ id: 's-1', alias: 'dev', kind: 'cmd' as const, command: 'npm run dev' }]
    expect(validateDraft({ name: ' build ', text: ' npm run build ' }, rows)).toEqual({ name: 'build', text: 'npm run build' })
    expect(() => validateDraft({ name: 'dev', text: 'x' }, rows)).toThrow('duplicate')
    expect(() => validateDraft({ name: 'dev', text: 'x' }, rows, 's-1')).not.toThrow()
    expect(() => validateDraft({ name: '', text: 'x' }, rows)).toThrow('non-empty')
    expect(() => validateDraft({ name: 'ok', text: '  ' }, rows)).toThrow('non-empty')
  })
})
