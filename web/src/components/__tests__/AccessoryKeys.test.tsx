import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { fireEvent, render, screen, waitFor } from '@testing-library/react'
import { AccessoryKeys } from '@/components/terminal/AccessoryKeys'
import { TERM_INPUT_EVENT, TERM_PASTE_EVENT } from '@/components/terminal/QuickKeys'
import { mockFetch } from '@/test/mockFetch'

describe('AccessoryKeys', () => {
  beforeEach(() => {
    vi.stubGlobal('fetch', mockFetch((url) => url === '/api/projects/p1' ? {
      status: 200,
      body: { shortcuts: [
        { id: 'custom-1', alias: 'Undo', kind: 'keys', keys: 'Ctrl-Z' },
        { id: 'command-1', alias: 'Build', kind: 'cmd', command: 'npm run build' },
      ] },
    } : undefined))
  })

  afterEach(() => {
    vi.restoreAllMocks()
    vi.unstubAllGlobals()
    delete (navigator as { clipboard?: Clipboard }).clipboard
  })

  it('sends fixed and user-defined key buttons through the terminal input event', async () => {
    const received: string[] = []
    const runShortcut = vi.fn()
    const onInput = (event: Event) => received.push((event as CustomEvent<string>).detail)
    window.addEventListener(TERM_INPUT_EVENT, onInput)
    render(<AccessoryKeys projectId="p1" onRunShortcut={runShortcut} />)

    fireEvent.click(screen.getByRole('button', { name: 'Esc' }))
    await screen.findByRole('button', { name: 'Undo' })
    fireEvent.click(screen.getByRole('button', { name: 'Undo' }))

    expect(received).toEqual(['\x1b'])
    expect(runShortcut).toHaveBeenCalledWith({ id: 'custom-1', alias: 'Undo', kind: 'keys', keys: 'Ctrl-Z' })
    expect(screen.getByRole('button', { name: 'Build' })).toBeTruthy()
    expect(screen.queryByRole('button', { name: 'Paste' })).toBeNull()
    window.removeEventListener(TERM_INPUT_EVENT, onInput)
  })

  it('keeps fixed and user-defined keys visible without a hide control', async () => {
    render(<AccessoryKeys projectId="p1" onRunShortcut={vi.fn()} />)
    await screen.findByRole('button', { name: 'Undo' })

    expect(screen.getByRole('button', { name: 'Esc' })).toBeTruthy()
    expect(screen.getByRole('button', { name: 'Undo' })).toBeTruthy()
    expect(screen.getByRole('button', { name: 'Build' })).toBeTruthy()
    expect(screen.queryByTestId('accessory-keys-toggle')).toBeNull()
  })

  it('uses xterm paste handling and hides Paste when clipboard read is unavailable', async () => {
    const readText = vi.fn().mockResolvedValue('pasted text')
    Object.defineProperty(navigator, 'clipboard', { configurable: true, value: { readText } })
    const paste = vi.fn()
    const onPaste = (event: Event) => paste((event as CustomEvent<string>).detail)
    window.addEventListener(TERM_PASTE_EVENT, onPaste)
    render(<AccessoryKeys projectId="p1" onRunShortcut={vi.fn()} />)
    fireEvent.click(screen.getByRole('button', { name: 'Paste' }))

    await waitFor(() => expect(paste).toHaveBeenCalledWith('pasted text'))
    expect(readText).toHaveBeenCalledOnce()
    window.removeEventListener(TERM_PASTE_EVENT, onPaste)
  })

  it('does not show Paste without clipboard read support', () => {
    render(<AccessoryKeys projectId="p1" onRunShortcut={vi.fn()} />)
    expect(screen.queryByRole('button', { name: 'Paste' })).toBeNull()
  })
})
