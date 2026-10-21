import { describe, expect, it } from 'vitest'

import { sendTermInput, TERM_INPUT_EVENT } from '@/components/terminal/QuickKeys'

describe('term input transport', () => {
  it('dispatches the sequence and target session as the event detail', () => {
    let got: unknown = null
    window.addEventListener(TERM_INPUT_EVENT, (e) => { got = (e as CustomEvent).detail }, { once: true })
    sendTermInput('\x03', 'main')
    expect(got).toEqual({ seq: '\x03', session: 'main' })
  })
})
