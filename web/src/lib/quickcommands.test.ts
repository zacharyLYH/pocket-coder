import { describe, expect, it } from 'vitest'

import { ALIAS_RE, validateQuickCommandRows } from '@/lib/quickcommands'

describe('quickcommands', () => {
  it('accepts the same alias shapes as Go aliasRe', () => {
    for (const a of ['dev', 'backend', 'a', 'A-b_9', 'x'.repeat(64)]) {
      expect(ALIAS_RE.test(a)).toBe(true)
    }
    for (const a of ['', '-lead', 'has space', 'sl/ash', 'x'.repeat(65)]) {
      expect(ALIAS_RE.test(a)).toBe(false)
    }
  })

  it('validates rows like the server', () => {
    expect(validateQuickCommandRows([{ alias: ' dev ', command: ' npm run dev ' }])).toEqual({ dev: 'npm run dev' })
    expect(() => validateQuickCommandRows([{ alias: '', command: 'x' }])).toThrow('non-empty')
    expect(() => validateQuickCommandRows([
      { alias: 'a', command: 'x' },
      { alias: 'a', command: 'y' },
    ])).toThrow('duplicate alias')
    expect(() => validateQuickCommandRows([{ alias: '-bad', command: 'x' }])).toThrow('invalid alias')
  })
})
