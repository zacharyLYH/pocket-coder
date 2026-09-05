export type QuickCommandRow = { alias: string; command: string }

// Must stay identical to Go's aliasRe in server/internal/project/projects.go
// (the server is the source of truth; this is only an early UI pre-filter).
export const ALIAS_RE = /^[a-zA-Z0-9][a-zA-Z0-9_-]{0,63}$/

// validateRows mirrors the server's per-row checks so the modal fails fast
// without a round trip. Returns the cleaned map or throws the first problem.
export function validateQuickCommandRows(rows: QuickCommandRow[]): Record<string, string> {
  const map: Record<string, string> = {}
  for (const r of rows) {
    const a = r.alias.trim()
    const c = r.command.trim()
    if (!a || !c) throw new Error('alias and command must be non-empty')
    if (map[a]) throw new Error(`duplicate alias: ${a}`)
    if (!ALIAS_RE.test(a)) throw new Error(`invalid alias: ${a}`)
    map[a] = c
  }
  return map
}
