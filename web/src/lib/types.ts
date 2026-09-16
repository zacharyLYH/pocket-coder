// Shared API shapes. One declaration per backend payload, imported
// everywhere instead of re-declared per component.
export type Project = { id: string; harnesses?: string[] }
export type SSHKey = { fingerprint: string; publicKey: string; label: string }
export type Harness = { id: string; name: string; command: string; install?: string; installed?: boolean }
export type ExecResult = { project: string; status: 'ok' | 'skipped' | 'error'; detail?: string }
export type GitFileStatus = {
  path: string
  staged: string
  unstaged: string
  stagedAdd: number
  stagedDel: number
  unstagedAdd: number
  unstagedDel: number
  binary: boolean
}
export type GitStatusResponse = { branch: string; files: GitFileStatus[]; notRepo?: boolean }
export type GitDiffResponse = {
  path: string
  diff: string
  truncated: boolean
  oldContent: string
  newContent: string
  contentsTruncated: boolean
  binary: boolean
}

export type AIConfigStatus = { baseURL: string; model: string; configured: boolean }
export type CodemapRef = { path: string; startLine: number; endLine: number; snippet: string; function?: string }
export type CodemapSection = { title: string; summary: string; refs: CodemapRef[] }
export type CodemapToolCall = { tool: string; args: string; output?: string; error?: string }
export type CodemapTurn = { turnId: string; sha: string; prompt: string; sections: CodemapSection[] | null; tools?: CodemapToolCall[] | null; time?: string }
export type CodemapThreadSummary = { id: string; title: string; createdAt: string; updatedAt: string; turnCount: number; preview: string }
export type CodemapThread = { id: string; project: string; title: string; createdAt: string; updatedAt: string; turns: CodemapTurn[] }
export type CodemapFile = { path: string; content: string; binary: boolean; moved: boolean; sha: string }

// isLaunchable reports whether a harness offers its own session type in
// "+ New Tab". The bash shell is a plain terminal, not a launch target.
export const isLaunchable = (h: Harness) => h.command !== 'bash'
