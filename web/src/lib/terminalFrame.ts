// Safe parser for binary terminal frames from server/internal/httpapi/terminal.go.
// Malformed messages return null; they must never throw inside the socket handler.
export type TerminalFrame =
  | { type: 'output'; data: string }
  | { type: 'exit'; code?: number; data?: string }

const OP_OUTPUT = 0x00
const OP_EXIT = 0x01
const decoder = new TextDecoder()

export function parseTerminalFrame(raw: ArrayBuffer | Uint8Array | string): TerminalFrame | null {
  if (typeof raw === 'string') return null
  const bytes = raw instanceof Uint8Array ? raw : new Uint8Array(raw)
  if (bytes.length < 1) return null
  if (bytes[0] === OP_OUTPUT) {
    return { type: 'output', data: decoder.decode(bytes.subarray(1)) }
  }
  if (bytes[0] === OP_EXIT) {
    if (bytes.length < 5) return null
    const view = new DataView(bytes.buffer, bytes.byteOffset + 1, 4)
    const code = view.getInt32(0, false)
    const data = bytes.length > 5 ? decoder.decode(bytes.subarray(5)) : undefined
    return { type: 'exit', code, data }
  }
  return null
}
