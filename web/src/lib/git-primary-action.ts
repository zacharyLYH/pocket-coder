// One primary action for the Git tab. Mirrors Orca's source-control CTA
// decision: the button label always states what pressing it will do, so the
// user never has to infer between Commit and Push. Pure and dependency-free
// so the mobile/desktop UI and unit tests share the exact same rules.

export type GitPrimaryActionKind =
  | 'stage-all'
  | 'commit'
  | 'commit-push'
  | 'push'
  | 'pull'
  | 'noop'

export type GitPrimaryAction = {
  kind: GitPrimaryActionKind
  label: string
  disabled: boolean
  hint?: string
}

export type GitPrimaryActionInputs = {
  stagedCount: number
  /** Files that can still be staged (unstaged tracked + untracked). */
  unstagedCount: number
  /** True when the commit message has non-whitespace content. */
  hasMessage: boolean
  ahead: number
  behind: number
  unborn: boolean
  hasUnresolvedConflicts: boolean
  conflictOperation?: 'merge' | 'rebase' | ''
  busy?: boolean
}

/** resolvePrimaryAction picks the one action that moves the repo forward. */
export function resolvePrimaryAction(i: GitPrimaryActionInputs): GitPrimaryAction {
  if (i.busy) {
    return { kind: 'noop', label: 'Working…', disabled: true }
  }
  if (i.hasUnresolvedConflicts) {
    const op = i.conflictOperation === 'merge' || i.conflictOperation === 'rebase'
      ? ` (${i.conflictOperation} in progress)`
      : ''
    return {
      kind: 'commit',
      label: 'Resolve conflicts',
      disabled: true,
      hint: `Resolve conflicts in the terminal${op}, then commit.`,
    }
  }
  if (i.stagedCount > 0 && i.hasMessage) {
    // An unborn HEAD has no upstream to push to; commit locally only.
    if (i.ahead > 0 && !i.unborn) {
      return { kind: 'commit-push', label: 'Commit & Push', disabled: false }
    }
    return { kind: 'commit', label: 'Commit', disabled: false }
  }
  if (i.stagedCount > 0) {
    return { kind: 'commit', label: 'Commit', disabled: true, hint: 'Enter a commit message.' }
  }
  if (i.unstagedCount > 0) {
    return { kind: 'stage-all', label: 'Stage all', disabled: false }
  }
  if (i.ahead > 0) {
    return { kind: 'push', label: 'Push', disabled: false }
  }
  if (i.behind > 0) {
    return { kind: 'pull', label: 'Pull', disabled: false }
  }
  if (i.unborn) {
    return { kind: 'noop', label: 'Commit first', disabled: true, hint: 'This repo has no commits yet.' }
  }
  return { kind: 'noop', label: 'Up to date', disabled: true, hint: 'Working tree clean and in sync.' }
}
