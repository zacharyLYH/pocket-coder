// Transport for synthetic terminal input: the shortcuts modal fires this
// event and TerminalPane forwards the sequence to the PTY over the
// existing websocket input path (no new transport).
export const TERM_INPUT_EVENT = 'pcoder-term-input'

export function sendTermInput(seq: string) {
  window.dispatchEvent(new CustomEvent<string>(TERM_INPUT_EVENT, { detail: seq }))
}
