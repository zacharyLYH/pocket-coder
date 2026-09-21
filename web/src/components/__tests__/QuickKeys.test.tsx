import { describe, expect, it } from 'vitest'
import { fireEvent, render, screen } from '@testing-library/react'

import { QuickKeys, TERM_INPUT_EVENT } from '@/components/terminal/QuickKeys'

describe('QuickKeys', () => {
  it('sends synthetic sequences over the terminal input event', () => {
    render(<QuickKeys />)
    let got: string | null = null
    window.addEventListener(TERM_INPUT_EVENT, (e) => { got = (e as CustomEvent<string>).detail }, { once: true })
    fireEvent.click(screen.getByTestId('qk-esc'))
    expect(got).toBe('\x1b')
  })

  it('sticky Ctrl turns the next character key into its ctrl variant', () => {
    render(<QuickKeys />)
    const seen: string[] = []
    window.addEventListener(TERM_INPUT_EVENT, (e) => { seen.push((e as CustomEvent<string>).detail) })
    fireEvent.click(screen.getByTestId('qk-ctrl'))
    expect(screen.getByTestId('qk-ctrl')).toHaveAttribute('aria-pressed', 'true')
    fireEvent.keyDown(window, { key: 'c' })
    expect(seen).toEqual(['\x03'])
    // Disarmed after one use.
    expect(screen.getByTestId('qk-ctrl')).toHaveAttribute('aria-pressed', 'false')
  })

  it('hides while a non-terminal text input owns focus', () => {
    render(
      <div>
        <QuickKeys />
        <textarea data-testid="notes" />
      </div>,
    )
    expect(screen.getByTestId('quick-keys')).toBeTruthy()
    screen.getByTestId('notes').focus()
    fireEvent.focusIn(screen.getByTestId('notes'))
    expect(screen.queryByTestId('quick-keys')).toBeNull()
  })
})
