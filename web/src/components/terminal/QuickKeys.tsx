// Transport for synthetic terminal input: the shortcuts modal and the
// accessory bar fire these events and the matching TerminalPane forwards
// them to its PTY over the existing websocket input path (no new
// transport). The target session scopes delivery when more than one pane
// could be listening.
export const TERM_INPUT_EVENT = 'pcoder-term-input'
export const TERM_PASTE_EVENT = 'pcoder-term-paste'
export const TERM_SHORTCUTS_CHANGED_EVENT = 'pcoder-term-shortcuts-changed'

export type TermInputDetail = { seq: string; session?: string }
export type TermPasteDetail = { text: string; session?: string }

export function sendTermInput(seq: string, session?: string) {
  window.dispatchEvent(new CustomEvent<TermInputDetail>(TERM_INPUT_EVENT, { detail: { seq, session } }))
}

export function pasteTermText(text: string, session?: string) {
  window.dispatchEvent(new CustomEvent<TermPasteDetail>(TERM_PASTE_EVENT, { detail: { text, session } }))
}
