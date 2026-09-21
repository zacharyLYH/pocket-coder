import { describe, expect, it } from 'vitest'

import { sendTermInput, TERM_INPUT_EVENT } from '@/components/terminal/QuickKeys'

describe('term input transport', () => {
  it('dispatches the sequence as the event detail', () => {
    let got: string | null = null
    window.addEventListener(TERM_INPUT_EVENT, (e) => { got = (e as CustomEvent<string>).detail }, { once: true })
    sendTermInput('\x03')
    expect(got).toBe('\x03')
  })
})
