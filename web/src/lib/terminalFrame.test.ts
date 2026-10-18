import { describe, expect, it } from 'vitest'
import { parseTerminalFrame } from '@/lib/terminalFrame'

const outputFrame = (text: string) => {
  const payload = new TextEncoder().encode(text)
  const frame = new Uint8Array(payload.length + 1)
  frame[0] = 0
  frame.set(payload, 1)
  return frame
}

describe('parseTerminalFrame', () => {
  it('decodes ANSI controls, line endings, CJK and supplementary Unicode as output', () => {
    const text = '\x1b[32mPASS ✓\x1b[0m\r\n中文 👩🏽‍💻\tend'
    expect(parseTerminalFrame(outputFrame(text))).toEqual({ type: 'output', data: text })
  })

  it('parses signed big-endian exit codes and optional UTF-8 detail', () => {
    expect(parseTerminalFrame(new Uint8Array([1, 0, 0, 0, 3]))).toEqual({
      type: 'exit', code: 3, data: undefined,
    })
    expect(parseTerminalFrame(new Uint8Array([1, 0xff, 0xff, 0xff, 0xff, ...new TextEncoder().encode('échec')])))
      .toEqual({ type: 'exit', code: -1, data: 'échec' })
  })

  it('respects Uint8Array offsets when parsing a view into a larger buffer', () => {
    const padded = new Uint8Array([99, ...outputFrame('🙂'), 99])
    expect(parseTerminalFrame(padded.subarray(1, -1))).toEqual({ type: 'output', data: '🙂' })
  })

  it('replaces malformed UTF-8 like the previous JSON string path', () => {
    expect(parseTerminalFrame(new Uint8Array([0, 0xff]))).toEqual({ type: 'output', data: '\ufffd' })
  })

  it('ignores malformed, unknown, and legacy text frames', () => {
    for (const raw of [
      '',
      'not json',
      JSON.stringify({ type: 'output', data: 'legacy' }),
      new Uint8Array([]),
      new Uint8Array([2, 1, 2]),
      new Uint8Array([1]),
      new Uint8Array([1, 0, 0, 0]),
    ]) {
      expect(parseTerminalFrame(raw), String(raw)).toBeNull()
    }
  })

  it('preserves the raw-wire bandwidth improvement for ANSI-heavy output', () => {
    const text = '\x1b[32mPASS\x1b[0m '.repeat(100)
    const binary = outputFrame(text)
    const legacy = new TextEncoder().encode(JSON.stringify({ type: 'output', data: text }))
    expect(parseTerminalFrame(binary)).toEqual({ type: 'output', data: text })
    expect(binary.byteLength).toBe(text.length + 1)
    expect(binary.byteLength).toBeLessThan(legacy.byteLength * 0.6)
  })
})
