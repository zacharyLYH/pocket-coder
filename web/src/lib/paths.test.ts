import { describe, expect, it } from 'vitest'

import { parsePreviewPath, parseTerminalPath, safeDecode, terminalPath } from '@/lib/paths'

describe('paths', () => {
  it('round-trips terminal paths', () => {
    expect(parseTerminalPath(terminalPath('proj/1', 'main'))).toEqual({ projectId: 'proj/1', session: 'main' })
  })

  it('rejects non-terminal paths', () => {
    expect(parseTerminalPath('/')).toBeNull()
    expect(parseTerminalPath('/preview/abc')).toBeNull()
  })

  it('never throws on malformed escapes', () => {
    expect(safeDecode('%zz')).toBeNull()
    expect(parseTerminalPath('/projects/%zz/terminal/main')).toBeNull()
    expect(parseTerminalPath('/projects/p/%zz')).toBeNull()
    expect(parsePreviewPath('/preview/%zz')).toBeNull()
  })

  it('parses preview ids', () => {
    expect(parsePreviewPath('/preview/abc123')).toBe('abc123')
    expect(parsePreviewPath('/')).toBeNull()
  })
})
