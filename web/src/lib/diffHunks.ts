// parseHunks splits raw unified diff text into its @@ hunks, dropping the
// `diff --git` / `---` / `+++` preamble. Each entry keeps its @@ header so
// it renders standalone and stages as an independent patch.
export function parseHunks(diff: string): string[] {
  const hunks: string[] = []
  let current: string[] | null = null
  for (const line of diff.split('\n')) {
    if (line.startsWith('@@ ')) {
      if (current) hunks.push(current.join('\n'))
      current = [line]
    } else if (current) {
      current.push(line)
    }
  }
  if (current) hunks.push(current.join('\n'))
  return hunks.filter((h) => h.trim() !== '')
}

// hunkPatch wraps one hunk in a minimal file patch git apply understands.
export function hunkPatch(path: string, hunk: string): string {
  return `diff --git a/${path} b/${path}\n--- a/${path}\n+++ b/${path}\n${hunk}\n`
}
