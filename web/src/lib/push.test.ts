import { describe, expect, it } from 'vitest'

import { decodeKey, keyToBase64, resolveClickTarget } from './push'

describe('push key encoding', () => {
  it('round-trips base64url', () => {
    const bytes = new Uint8Array([0, 1, 2, 250, 251, 252, 253, 254, 255])
    expect(decodeKey(keyToBase64(bytes))).toEqual(bytes)
  })

  it('accepts unpadded and padded input', () => {
    expect(decodeKey('AQID')).toEqual(new Uint8Array([1, 2, 3]))
    expect(decodeKey('AQIDBA')).toEqual(new Uint8Array([1, 2, 3, 4]))
  })
})

describe('resolveClickTarget', () => {
  const origin = 'https://coder.example'

  it('accepts a same-origin path', () => {
    expect(resolveClickTarget('/app', origin)).toBe('https://coder.example/app')
  })

  it('accepts an absolute same-origin URL', () => {
    expect(resolveClickTarget('https://coder.example/app?x=1', origin)).toBe('https://coder.example/app?x=1')
  })

  it('rejects a foreign origin', () => {
    expect(resolveClickTarget('https://evil.example/app', origin)).toBeNull()
  })

  it('rejects a protocol-relative foreign origin', () => {
    expect(resolveClickTarget('//evil.example/app', origin)).toBeNull()
  })

  it('rejects garbage', () => {
    expect(resolveClickTarget('http://[', origin)).toBeNull()
  })
})
