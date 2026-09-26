import { describe, expect, it } from 'vitest'

import { errMsg, probeErr, probeSignal } from '@/lib/api'

describe('probe helpers', () => {
  it('maps aborts to a timeout hint and passes other errors through', () => {
    const timeout = new DOMException('The operation timed out.', 'TimeoutError')
    expect(probeErr(timeout)).toContain('Timed out after 90s')
    expect(probeErr(new Error('boom'))).toBe('boom')
    expect(errMsg(timeout)).not.toContain('Timed out')
  })

  it('returns a live signal', () => {
    const s = probeSignal()
    expect(s).toBeInstanceOf(AbortSignal)
    expect(s.aborted).toBe(false)
  })
})
