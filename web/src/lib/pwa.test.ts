import { existsSync, readFileSync } from 'node:fs'
import { dirname, join } from 'node:path'
import { fileURLToPath } from 'node:url'
import { describe, expect, it } from 'vitest'

// PWA installability pin: Chromium requires a manifest with name,
// short_name, start_url, display, theme color, icons >= 144px, and a
// registered service worker with a fetch handler. These guard the
// static contract so a refactor can't silently drop installability.
const root = join(dirname(fileURLToPath(import.meta.url)), '..', '..')
const pub = (p: string) => join(root, 'public', p)

function pngSize(path: string): { w: number; h: number } {
  const b = readFileSync(path)
  const magic = Array.from(b.subarray(0, 8)).join(',')
  if (magic !== '137,80,78,71,13,10,26,10') throw new Error(`${path}: not a PNG`)
  const v = new DataView(b.buffer, b.byteOffset, b.byteLength)
  return { w: v.getUint32(16), h: v.getUint32(20) }
}

describe('pwa static contract', () => {
  it('ships a valid installable manifest', () => {
    const raw = readFileSync(pub('manifest.webmanifest'), 'utf8')
    const m = JSON.parse(raw) as {
      name?: string
      short_name?: string
      start_url?: string
      id?: string
      display?: string
      theme_color?: string
      icons?: { src: string; sizes: string; type: string; purpose?: string }[]
    }
    expect(m.name).toBeTruthy()
    expect(m.short_name).toBeTruthy()
    // The app lives at /app (/ is the landing page), so an installed PWA
    // must launch there — otherwise every launch flashes the landing page.
    expect(m.start_url).toBe('/app')
    expect(m.id).toBe('/app')
    expect(m.display).toBe('standalone')
    expect(m.theme_color).toBeTruthy()
    const icons = m.icons ?? []
    expect(Math.max(...icons.map((i) => parseInt(i.sizes, 10)))).toBeGreaterThanOrEqual(512)
    expect(icons.some((i) => parseInt(i.sizes, 10) >= 144)).toBe(true)
    expect(icons.some((i) => (i.purpose ?? 'any').includes('maskable'))).toBe(true)
    for (const i of icons) {
      // one declared size per icon, and the file must match it exactly
      expect(i.sizes).toMatch(/^\d+x\d+$/)
      const file = join(root, 'public', i.src.replace(/^\//, ''))
      expect(existsSync(file), `missing icon ${i.src}`).toBe(true)
      const { w, h } = pngSize(file)
      expect(`${w}x${h}`).toBe(i.sizes)
    }
  })

  it('ships an apple touch icon referenced by index.html', () => {
    expect(existsSync(pub('apple-touch-icon.png'))).toBe(true)
    expect(pngSize(pub('apple-touch-icon.png'))).toEqual({ w: 180, h: 180 })
    const html = readFileSync(join(root, 'index.html'), 'utf8')
    expect(html).toContain('manifest.webmanifest')
    expect(html).toContain('apple-touch-icon')
    expect(html).toContain('theme-color')
  })

  it('ships a service worker with a fetch handler that bypasses api/ws', () => {
    expect(existsSync(pub('sw.js'))).toBe(true)
    const sw = readFileSync(pub('sw.js'), 'utf8')
    expect(sw).toContain(`'fetch'`)
    expect(sw).toContain('/api')
    expect(sw).toContain('/ws')
  })
})
