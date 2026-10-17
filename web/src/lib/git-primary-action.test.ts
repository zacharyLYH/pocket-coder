import { describe, expect, it } from 'vitest'

import { resolvePrimaryAction, type GitPrimaryActionInputs } from './git-primary-action'

const base: GitPrimaryActionInputs = {
  stagedCount: 0,
  unstagedCount: 0,
  hasMessage: false,
  ahead: 0,
  behind: 0,
  unborn: false,
  hasUnresolvedConflicts: false,
}

const resolve = (patch: Partial<GitPrimaryActionInputs>) => resolvePrimaryAction({ ...base, ...patch })

describe('resolvePrimaryAction', () => {
  it('offers Stage all when only unstaged changes exist', () => {
    expect(resolve({ unstagedCount: 2 })).toMatchObject({ kind: 'stage-all', label: 'Stage all', disabled: false })
  })

  it('offers Commit once staged with a message', () => {
    expect(resolve({ stagedCount: 1, hasMessage: true })).toMatchObject({ kind: 'commit', label: 'Commit', disabled: false })
  })

  it('offers Commit & Push when staged, messaged, and ahead', () => {
    expect(resolve({ stagedCount: 1, hasMessage: true, ahead: 2 }))
      .toMatchObject({ kind: 'commit-push', label: 'Commit & Push', disabled: false })
  })

  it('disables Commit until a message exists', () => {
    expect(resolve({ stagedCount: 1, hasMessage: false }))
      .toMatchObject({ kind: 'commit', label: 'Commit', disabled: true, hint: 'Enter a commit message.' })
  })

  it('offers Push on a clean branch ahead of upstream', () => {
    expect(resolve({ ahead: 3 })).toMatchObject({ kind: 'push', label: 'Push', disabled: false })
  })

  it('offers Pull on a clean branch behind upstream', () => {
    expect(resolve({ behind: 3 })).toMatchObject({ kind: 'pull', label: 'Pull', disabled: false })
  })

  it('prefers Push when a clean branch is both ahead and behind', () => {
    expect(resolve({ ahead: 1, behind: 1 }).kind).toBe('push')
  })

  it('blocks commit while conflicts are unresolved', () => {
    expect(resolve({ stagedCount: 1, hasMessage: true, hasUnresolvedConflicts: true, conflictOperation: 'merge' }))
      .toMatchObject({ kind: 'commit', label: 'Resolve conflicts', disabled: true })
  })

  it('shows Up to date on a clean, synced tree', () => {
    expect(resolve({})).toMatchObject({ kind: 'noop', label: 'Up to date', disabled: true })
  })

  it('shows Commit first on an unborn repo with no files', () => {
    expect(resolve({ unborn: true })).toMatchObject({ kind: 'noop', label: 'Commit first', disabled: true })
  })

  it('still commits the first commit in an unborn repo', () => {
    expect(resolve({ unborn: true, stagedCount: 1, hasMessage: true }))
      .toMatchObject({ kind: 'commit', label: 'Commit', disabled: false })
  })

  it('disables the CTA while a git operation is running', () => {
    expect(resolve({ busy: true, stagedCount: 1, hasMessage: true }))
      .toMatchObject({ kind: 'noop', label: 'Working…', disabled: true })
  })
})
