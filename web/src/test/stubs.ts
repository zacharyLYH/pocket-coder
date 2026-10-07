import { vi } from 'vitest'

// MockWebSocket is the minimal WebSocket double both terminal tests share:
// captures open/message/close without auto-opening, so tests take the
// socket live explicitly via instances[n].open().
export class MockWebSocket {
  static CONNECTING = 0
  static OPEN = 1
  static CLOSING = 2
  static CLOSED = 3
  static instances: MockWebSocket[] = []
  readyState = 0
  onopen: (() => void) | null = null
  onmessage: ((ev: MessageEvent) => void) | null = null
  onclose: (() => void) | null = null
  onerror: (() => void) | null = null
  sent: string[] = []
  constructor(public url: string) {
    MockWebSocket.instances.push(this)
  }
  open() {
    this.readyState = 1
    this.onopen?.()
  }
  send(data: string) { this.sent.push(data) }
  close() { this.readyState = 3; this.onclose?.() }
}

// stubWebSocket resets the instance list and installs the double as the
// global WebSocket. Returns the live list tests assert against.
export function stubWebSocket(): MockWebSocket[] {
  MockWebSocket.instances = []
  vi.stubGlobal('WebSocket', MockWebSocket as unknown as typeof WebSocket)
  return MockWebSocket.instances
}

// stubMatchMedia installs the standard matchMedia double. Most suites only
// care about the matches bit; query-dependent behavior stays hand-rolled.
export function stubMatchMedia(matches = false) {
  Object.defineProperty(window, 'matchMedia', {
    writable: true,
    value: vi.fn().mockReturnValue({ matches, addEventListener: vi.fn(), removeEventListener: vi.fn() }),
  })
}
