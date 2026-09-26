import { describe, expect, it, vi } from 'vitest'
import { render, screen, fireEvent, waitFor } from '@testing-library/react'
import { AICard } from '@/components/AICard'
import { mockFetch } from '@/test/mockFetch'

// Saving a model must refresh the parent (Home row, codemap tab) through
// onChanged — otherwise the UI still shows "not set" until a reload.
describe('AICard', () => {
  function stub(initial: unknown[] = []) {
    let rows = initial
    const calls: string[] = []
    const fetchMock = mockFetch((url, init) => {
      calls.push(`${init?.method ?? 'GET'} ${url}`)
      if (url === '/api/ai/models' && (init?.method ?? 'GET') === 'GET') {
        return { status: 200, body: { models: rows } }
      }
      if (url === '/api/ai/models' && init?.method === 'POST') {
        rows = ROW
        return { status: 201, body: { id: 'm1' } }
      }
      if (url === '/api/ai/models/m1' && init?.method === 'PUT') return { status: 200, body: { ok: true } }
      if (url === '/api/ai/models/m1' && init?.method === 'DELETE') {
        rows = []
        return { status: 200, body: { ok: true } }
      }
      return undefined
    })
    vi.stubGlobal('fetch', fetchMock)
    return calls
  }

  const ROW = [{ id: 'm1', label: 'mini', baseURL: 'https://x', model: 'm', hasKey: true }]

  async function fill() {
    fireEvent.change(screen.getByTestId('ai-label'), { target: { value: 'mini' } })
    fireEvent.change(screen.getByTestId('ai-base-url'), { target: { value: 'https://x' } })
    fireEvent.change(screen.getByTestId('ai-api-key'), { target: { value: 'k' } })
    fireEvent.change(screen.getByTestId('ai-model'), { target: { value: 'm' } })
  }

  it('saves without a prior test and lists the new row', async () => {
    stub()
    const onChanged = vi.fn()
    render(<AICard onChanged={onChanged} />)
    await screen.findByText(/No models yet/)

    // No Test click: Save alone probes once server-side.
    await fill()
    fireEvent.click(screen.getByTestId('ai-save'))

    await waitFor(() => expect(onChanged).toHaveBeenCalled())
    expect(await screen.findByDisplayValue('mini')).toBeInTheDocument()
  })

  it('confirms deletes with the blast radius', async () => {
    stub(ROW)
    const onChanged = vi.fn()
    render(<AICard onChanged={onChanged} />)
    await screen.findByDisplayValue('mini')

    fireEvent.click(screen.getByRole('button', { name: 'Delete' }))
    // The row is still there until Confirm.
    expect(screen.getByDisplayValue('mini')).toBeInTheDocument()
    expect(await screen.findByText(/Codemaps and the butler stop/)).toBeInTheDocument()
    fireEvent.click(screen.getByTestId('ai-confirm-delete'))

    await waitFor(() => expect(onChanged).toHaveBeenCalled())
    expect(screen.queryByDisplayValue('mini')).not.toBeInTheDocument()
    await screen.findByText(/No models yet/)
  })

  it('renames a row through the label input', async () => {
    const calls = stub(ROW)
    render(<AICard />)
    await screen.findByDisplayValue('mini')

    const input = screen.getByLabelText('Label for m')
    fireEvent.change(input, { target: { value: 'renamed' } })
    fireEvent.blur(input)

    await waitFor(() => expect(calls).toContain('PUT /api/ai/models/m1'))
  })
})
