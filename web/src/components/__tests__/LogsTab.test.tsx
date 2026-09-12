import { render, screen, waitFor } from '@testing-library/react'
import { describe, expect, it, vi, beforeEach } from 'vitest'

import { LogsTab } from '@/components/terminal/LogsTab'

describe('LogsTab', () => {
  beforeEach(() => {
    vi.unstubAllGlobals()
  })

  it('tails the project running log', async () => {
    vi.stubGlobal('fetch', vi.fn(async (url: string) => {
      expect(String(url)).toContain('/api/projects/abc/logs')
      return new Response(JSON.stringify({ logs: [
        { id: 1, time: new Date().toISOString(), type: 'preview.start', message: 'Preview started on :3000' },
        { id: 2, time: new Date().toISOString(), type: 'preview.open', message: 'Preview opened' },
      ] }), { status: 200 })
    }))
    render(<LogsTab projectId="abc" />)
    await waitFor(() => expect(screen.getByTestId('logs-list')).toBeVisible())
    expect(screen.getByTestId('logs-list')).toHaveTextContent('Preview started on :3000')
    expect(screen.getByTestId('logs-list')).toHaveTextContent('preview.open')
  })

  it('shows an empty state with no logs', async () => {
    vi.stubGlobal('fetch', vi.fn(async () => new Response(JSON.stringify({ logs: [] }), { status: 200 })))
    render(<LogsTab projectId="abc" />)
    await waitFor(() => expect(screen.getByTestId('logs-empty')).toBeVisible())
  })
})
