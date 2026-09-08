// Safe parsing for terminal WebSocket frames (see TerminalPane and
// server/internal/httpapi/terminal.go). The server speaks JSON text frames,
// but proxies, extensions, or a half-closed socket can deliver anything —
// a single malformed message must never throw inside the socket handler and
// take the terminal down with it. Malformed input returns null: ignore it.
export type TerminalFrame =
  | { type: 'output'; data: string }
  | { type: 'exit'; code?: number; data?: string }

export function parseTerminalFrame(raw: string): TerminalFrame | null {
  let parsed: unknown
  try {
    parsed = JSON.parse(raw)
  } catch {
    return null
  }
  if (typeof parsed !== 'object' || parsed === null) return null
  const type = (parsed as { type?: unknown }).type
  if (type === 'output') {
    const data = (parsed as { data?: unknown }).data
    return { type: 'output', data: typeof data === 'string' ? data : '' }
  }
  if (type === 'exit') {
    const { code, data } = parsed as { code?: unknown; data?: unknown }
    return {
      type: 'exit',
      code: typeof code === 'number' ? code : undefined,
      data: typeof data === 'string' ? data : undefined,
    }
  }
  return null
}
