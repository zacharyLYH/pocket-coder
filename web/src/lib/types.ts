// One shared agent-step shape both products persist
// {tool, args, output?, error?}; butler summarizes outputs shorter.
export type AgentStep = { tool: string; args?: string; output?: string; error?: string }
export type ThreadStatus = 'ready' | 'running' | 'awaiting' | 'failed'
export type ButlerTurn = { turnId: string; prompt: string; answer?: string; steps?: AgentStep[] | null; projectHint?: string; time?: string; error?: string | null }
export type ButlerThreadSummary = { id: string; title: string; createdAt: string; updatedAt: string; turnCount: number; preview: string; status?: ThreadStatus }
export type ButlerThread = { id: string; title: string; createdAt: string; updatedAt: string; turns: ButlerTurn[]; approvals?: ButlerConfirm[]; status?: ThreadStatus }
export type ButlerConfirm = { id: string; tool: string; summary: string; blastRadius: string }
export type ButlerTurnResult = { threadId: string; threadTitle: string; turnId: string; answer: string; steps: AgentStep[]; time: string }

// Shared API shapes.
export type Project = { id: string; harnesses?: string[] }
export type Harness = { id: string; name: string; command: string; install?: string; installed?: boolean; installing?: boolean }
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
export type GitUpstream = { name: string; ahead: number; behind: number } | null
export type GitStatusResponse = { branch: string; files: GitFileStatus[]; notRepo?: boolean; upstream?: GitUpstream; unborn?: boolean }
export type GitBranchList = {
  current: string
  detached: boolean
  local: { name: string; relativeTime: string }[]
  remote: { name: string; relativeTime: string }[]
}
export type GitDiffResponse = {
  path: string
  diff: string
  truncated: boolean
  oldContent: string
  newContent: string
  contentsTruncated: boolean
  binary: boolean
}

export type AIConfigStatus = { model: string; configured: boolean }
export type AIModel = { id: string; label: string; baseURL: string; model: string; hasKey: boolean }
export type CodemapRef = { path: string; startLine: number; endLine: number; snippet: string; function?: string }
export type CodemapSection = { title: string; summary: string; refs: CodemapRef[] }
export type CodemapTurn = { turnId: string; sha: string; prompt: string; sections: CodemapSection[] | null; steps?: AgentStep[] | null; time?: string; error?: string | null }
export type CodemapThreadSummary = { id: string; title: string; createdAt: string; updatedAt: string; turnCount: number; preview: string; status?: ThreadStatus }
export type CodemapThread = { id: string; project: string; title: string; createdAt: string; updatedAt: string; turns: CodemapTurn[]; status?: ThreadStatus }
export type CodemapFile = { path: string; content: string; binary: boolean; moved: boolean; sha: string }

// isLaunchable: the bash shell is a plain terminal, not a launch target.
export const isLaunchable = (h: Harness) => h.command !== 'bash'
