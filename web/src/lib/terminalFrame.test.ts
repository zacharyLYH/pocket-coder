import { describe, expect, it } from 'vitest'
import { parseTerminalFrame } from '@/lib/terminalFrame'

// The terminal socket can deliver non-JSON bytes (proxy error pages,
// extensions, half-closed frames). Parsing must never throw: malformed
// input is ignored, valid frames pass through.
describe('parseTerminalFrame', () => {
  it('parses output frames', () => {
    expect(parseTerminalFrame(JSON.stringify({ type: 'output', data: 'hi' }))).toEqual({
      type: 'output',
      data: 'hi',
    })
  })

  it('parses exit frames with code', () => {
    expect(parseTerminalFrame(JSON.stringify({ type: 'exit', code: 1 }))).toEqual({
      type: 'exit',
      code: 1,
      data: undefined,
    })
  })

  it('ignores malformed input instead of throwing', () => {
    for (const raw of [
      'not json',
      '',
      '<html>502 Bad Gateway</html>',
      '[1,2,3]',
      '"output"',
      'null',
      '{"type":"bogus"}',
    ]) {
      expect(parseTerminalFrame(raw), raw).toBeNull()
    }
  })

  it('coerces missing output data to empty string', () => {
    expect(parseTerminalFrame('{"type":"output"}')).toEqual({ type: 'output', data: '' })
    expect(parseTerminalFrame('{"type":"output","data":42}')).toEqual({ type: 'output', data: '' })
  })
})
