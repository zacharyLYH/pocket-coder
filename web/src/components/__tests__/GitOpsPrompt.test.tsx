import { describe, expect, it, vi } from 'vitest'
import { render, screen, fireEvent } from '@testing-library/react'
import { GitOpsPrompt } from '@/components/terminal/GitOpsPrompt'
import { mockFetch } from '@/test/mockFetch'

// GitOpsPrompt pins: the advanced-git section offers the three ops,
// collects the branch inputs, and shows the tailored backend prompt.
describe('GitOpsPrompt', () => {
  function stub(prompt: string) {
    vi.stubGlobal('fetch', mockFetch((url, init) => {
      if (url === '/api/projects/x%2Fhello/git/ops-prompt' && init?.method === 'POST') {
        const body = JSON.parse(String(init.body))
        expect(['pr', 'sync', 'undo']).toContain(body.op)
        return { status: 200, body: { prompt } }
      }
      return undefined
    }))
  }

  it('fetches a tailored PR prompt with the branch name', async () => {
    stub('move this work onto feat/login based on main')
    render(<GitOpsPrompt projectId="x/hello" branch="main" />)

    expect(screen.getByTestId('git-ops')).toHaveTextContent('runs in your terminal AI')
    fireEvent.change(screen.getByTestId('git-ops-new-branch'), { target: { value: 'feat/login' } })
    fireEvent.click(screen.getByTestId('git-ops-get'))

    expect(await screen.findByTestId('git-ops-prompt')).toHaveTextContent('feat/login')
  })

  it('switches ops and hides the branch input for undo', async () => {
    stub('confirm by typing the branch name')
    render(<GitOpsPrompt projectId="x/hello" branch="main" />)

    fireEvent.click(screen.getByTestId('git-ops-undo'))
    expect(screen.queryByTestId('git-ops-new-branch')).not.toBeInTheDocument()
    fireEvent.click(screen.getByTestId('git-ops-get'))

    expect(await screen.findByTestId('git-ops-prompt')).toHaveTextContent('confirm')
  })
})
