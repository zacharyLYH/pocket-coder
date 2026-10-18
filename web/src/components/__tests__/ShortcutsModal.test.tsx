import { describe, expect, it, vi } from 'vitest'
import { fireEvent, render, screen, waitFor } from '@testing-library/react'
import { ShortcutsModal } from '@/components/shortcuts/ShortcutsModal'
import { mockFetch } from '@/test/mockFetch'

// Opening the modal must NOT steal focus into the search box (on mobile
// that pops the keyboard over the list before the user asked to type).
describe('ShortcutsModal search focus', () => {
  it('leaves the search box unfocused on open', async () => {
    vi.stubGlobal('fetch', mockFetch((url) =>
      url === '/api/projects/p1' ? { status: 200, body: { shortcuts: [] } } : undefined))
    render(<ShortcutsModal projectId="p1" open onOpenChange={vi.fn()} />)
    const search = await waitFor(() => screen.getByLabelText('Search shortcuts'))
    await waitFor(() => expect(document.activeElement).not.toBe(search))
  })

  it('does not run a terminal shortcut when opening its Edit menu item', async () => {
    vi.stubGlobal('fetch', mockFetch((url) =>
      url === '/api/projects/p1' ? { status: 200, body: { shortcuts: [
        { id: 's-1', alias: 'build', kind: 'cmd', command: 'npm run build' },
      ] } } : undefined))
    const onRun = vi.fn()
    render(<ShortcutsModal projectId="p1" open onOpenChange={vi.fn()} onRun={onRun} />)

    const manage = await screen.findByTestId('sc-menu-build')
    fireEvent.pointerDown(manage, { button: 0 })
    fireEvent.click(manage)
    const edit = await screen.findByRole('menuitem', { name: 'Edit' })
    fireEvent.click(edit)

    expect(await screen.findByLabelText('Shortcut name')).toHaveValue('build')
    expect(onRun).not.toHaveBeenCalled()
  })
})
