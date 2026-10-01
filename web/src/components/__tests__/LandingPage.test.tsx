import { describe, expect, it, vi } from 'vitest'
import { render, screen, fireEvent } from '@testing-library/react'
import { LandingPage } from '@/components/LandingPage'

// The installer command is built from placeholders until the user types:
// copying it verbatim would validate, then die at the SMTP probe. The
// copy button stays disabled until both fields are filled.
describe('LandingPage setup builder', () => {
  it('disables copy until email and app password are filled', () => {
    render(<LandingPage onLogin={vi.fn()} />)
    const copy = screen.getByRole('button', { name: 'Copy command' })
    expect(copy).toBeDisabled()

    fireEvent.change(screen.getByLabelText('Gmail address'), { target: { value: 'me@gmail.com' } })
    expect(screen.getByRole('button', { name: 'Copy command' })).toBeDisabled()

    fireEvent.change(screen.getByLabelText('App password'), { target: { value: 'xxxx' } })
    expect(screen.getByRole('button', { name: 'Copy command' })).toBeEnabled()
  })
})
