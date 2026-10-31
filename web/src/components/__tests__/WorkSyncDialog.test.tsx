import { describe, expect, it, vi } from 'vitest'
import { render, screen } from '@testing-library/react'
import { WorkSyncDialog } from '@/components/WorkSyncDialog'
import { mockFetch } from '@/test/mockFetch'

// The dialog is pcoder's half of Work Sync: it shows a code the laptop
// enters. It never moves files, so its contract is display + request.
describe('WorkSyncDialog', () => {
  const CODE = 'ABCD-EFGH-JKMN-PQRS'

  function stub(opts: { fails?: boolean; status?: string } = {}) {
    vi.stubGlobal('fetch', mockFetch((url, init) => {
      if (url === '/api/projects/acme%2Fapi/sync' && (init?.method ?? 'GET') === 'GET') {
        return { status: 200, body: { status: opts.status ?? 'idle' } }
      }
      if (url === '/api/projects/acme%2Fapi/sync/code' && init?.method === 'POST') {
        if (opts.fails) return { status: 409, body: { error: { code: 'sync_running', message: 'Sync already running for this project.' } } }
        return {
          status: 200,
          body: {
            code: CODE,
            project: 'acme/api',
            expiresAt: new Date(Date.now() + 10 * 60_000).toISOString(),
            command: 'curl -fsSL https://pcoder.example/sync.sh | sh',
          },
        }
      }
      return undefined
    }))
  }

  it('shows the code, expiry, and laptop command', async () => {
    stub()
    render(<WorkSyncDialog projectId="acme/api" open onOpenChange={() => {}} />)
    expect(await screen.findByTestId('sync-code')).toHaveTextContent(CODE)
    expect(screen.getByTestId('sync-expiry')).toHaveTextContent(/Expires in 9:5\d/)
    expect(screen.getByTestId('sync-command')).toHaveTextContent('https://pcoder.example/sync.sh')
  })

  it('explains that Upload and Download overwrite, not merge', async () => {
    stub()
    render(<WorkSyncDialog projectId="acme/api" open onOpenChange={() => {}} />)
    await screen.findByTestId('sync-code')
    expect(screen.getByTestId('work-sync-dialog')).toHaveTextContent(/Upload.*overwrites pcoder/)
    expect(screen.getByTestId('work-sync-dialog')).toHaveTextContent(/Download.*overwrites this folder/)
  })

  it('asks the user to stop agent turns, since pcoder cannot detect one', async () => {
    stub()
    render(<WorkSyncDialog projectId="acme/api" open onOpenChange={() => {}} />)
    await screen.findByTestId('sync-code')
    expect(screen.getByTestId('sync-agent-warning')).toHaveTextContent(/Stop agent turns in this project first/)
    expect(screen.getByTestId('sync-agent-warning')).toHaveTextContent(/cannot detect a running agent/)
  })

  it('shows the live transfer so the user knows not to interact', async () => {
    stub({ status: 'uploading' })
    render(<WorkSyncDialog projectId="acme/api" open onOpenChange={() => {}} />)
    await screen.findByTestId('sync-code')
    expect(await screen.findByTestId('sync-status')).toHaveTextContent(/Uploading… don't touch this project/)
  })

  it('surfaces the server error message', async () => {
    stub({ fails: true })
    render(<WorkSyncDialog projectId="acme/api" open onOpenChange={() => {}} />)
    expect(await screen.findByTestId('sync-error')).toHaveTextContent('Sync already running for this project.')
  })
})
