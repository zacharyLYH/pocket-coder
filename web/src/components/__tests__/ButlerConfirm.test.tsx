import { describe, expect, it, vi, beforeEach } from 'vitest'
import { render, screen, fireEvent, waitFor } from '@testing-library/react'
import { ButlerConfirm } from '@/components/ButlerConfirm'
import type { ButlerConfirm as Card } from '@/lib/types'

const MODEL_CARD: Card = {
  id: 'c1',
  tool: 'create_ai_model',
  summary: 'Add AI model "Two"?',
  blastRadius: 'Adds one entry for model gpt-4o. The key is typed into a masked field at Confirm.',
}

const STOP_CARD: Card = {
  id: 'c2',
  tool: 'stop_project',
  summary: 'Stop project a/b?',
  blastRadius: 'Stops its container and preview. Sessions end.',
}

function stubFetch(calls: { url: string; body?: string }[]) {
  vi.stubGlobal(
    'fetch',
    vi.fn(async (url: string, init?: RequestInit) => {
      calls.push({ url: String(url), body: init?.body ? String(init.body) : undefined })
      return new Response(JSON.stringify({ ok: true, result: 'Added x' }), {
        status: 200,
        headers: { 'Content-Type': 'application/json' },
      })
    }),
  )
}

describe('ButlerConfirm', () => {
  beforeEach(() => {
    vi.unstubAllGlobals()
  })

  it('shows no value field for plain writes', () => {
    stubFetch([])
    render(<ButlerConfirm card={STOP_CARD} onDone={vi.fn()} />)
    expect(screen.queryByTestId('butler-confirm-value')).not.toBeInTheDocument()
  })

  it('masks the key field on create_ai_model and posts it only in the apply body', async () => {
    const calls: { url: string; body?: string }[] = []
    stubFetch(calls)
    const onDone = vi.fn()
    render(<ButlerConfirm card={MODEL_CARD} onDone={onDone} />)
    const input = screen.getByTestId('butler-confirm-value')
    expect(input).toHaveAttribute('type', 'password')
    // Confirm stays disabled until a key is typed.
    expect(screen.getByTestId('butler-confirm-ok')).toBeDisabled()
    fireEvent.change(input, { target: { value: 'test-key-two' } })
    fireEvent.click(screen.getByTestId('butler-confirm-ok'))
    await waitFor(() => expect(onDone).toHaveBeenCalledWith('Added x'))
    expect(calls).toHaveLength(1)
    expect(calls[0].url).toContain('/api/butler/confirms/c1/apply')
    expect(calls[0].body).toBe(JSON.stringify({ value: 'test-key-two' }))
  })
})
