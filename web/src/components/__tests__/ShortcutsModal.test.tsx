import { describe, expect, it, vi } from 'vitest'
import { render, screen, waitFor } from '@testing-library/react'
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
})
