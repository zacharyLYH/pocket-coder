// Shared API shapes. One declaration per backend payload, imported
// everywhere instead of re-declared per component.
export type Project = { id: string; name: string; harnesses?: string[] }
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

// isLaunchable reports whether a harness offers its own session type in
// "+ New Tab". The bash shell is a plain terminal, not a launch target.
export const isLaunchable = (h: Harness) => h.command !== 'bash'
