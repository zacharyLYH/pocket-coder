import { describe, expect, it } from 'vitest'
import { readFileSync } from 'node:fs'
import { dirname, join } from 'node:path'
import { fileURLToPath } from 'node:url'

// Static pin on the production proxy contract: the terminal websocket
// must not inherit nginx's 60s default read timeout (idle terminals would
// die with 1006 a minute after the last keystroke), and the fingerprinted
// bundle deserves compression + immutable caching.
const root = join(dirname(fileURLToPath(import.meta.url)), '..', '..')
const conf = readFileSync(join(root, 'nginx.conf'), 'utf8')

function blockAt(directive: string): string {
  const start = conf.indexOf(directive)
  expect(start, `missing ${directive}`).toBeGreaterThanOrEqual(0)
  return conf.slice(start, conf.indexOf('}', start))
}

describe('nginx prod contract', () => {
  it('gives the websocket location hour-long timeouts', () => {
    const ws = blockAt('location /ws')
    expect(ws).toContain('proxy_read_timeout')
    expect(ws).toContain('proxy_send_timeout')
  })

  it('forwards WebSocket upgrades through /api for the noVNC preview surface', () => {
    const api = blockAt('location /api')
    expect(api).toContain('proxy_http_version 1.1')
    expect(api).toContain('proxy_set_header Upgrade')
    expect(api).toContain('proxy_set_header Connection')
  })

  it('compresses responses and caches hashed assets immutably', () => {
    expect(conf).toContain('gzip on')
    const assets = blockAt('location /assets/')
    expect(assets).toContain('immutable')
  })
})
