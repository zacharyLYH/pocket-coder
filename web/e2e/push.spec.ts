import { execFileSync } from 'node:child_process'
import { createECDH, randomBytes } from 'node:crypto'
import { createServer, type IncomingMessage, type ServerResponse } from 'node:http'
import type { APIRequestContext, Page } from '@playwright/test'

import { ciTimeout, expect, test } from './test'
import { API_PORT, COMPOSE_PROJECT, DATA_DIR } from './env'

// Real backend + real store + real sender. Only the browser push API and
// the relay are faked: the Go container POSTs to a host-side relay through
// host.docker.internal, exactly as it would to FCM.
//
// The relay must use http on a local host; push.ValidateSubscription allows
// that for loopback and host.docker.internal only.

interface Captured {
  headers: IncomingMessage['headers']
  body: Buffer
}

function b64url(buf: Buffer): string {
  return buf.toString('base64').replace(/\+/g, '-').replace(/\//g, '_').replace(/=+$/, '')
}

// Client-side subscription keys, minted exactly as a browser would.
function clientKeys() {
  const ecdh = createECDH('prime256v1')
  ecdh.generateKeys()
  return { p256dh: b64url(ecdh.getPublicKey()), auth: b64url(randomBytes(16)) }
}

async function startRelay(status: number): Promise<{ endpoint: string; captured: Captured[]; close: () => Promise<void> }> {
  const captured: Captured[] = []
  const server = createServer((req: IncomingMessage, res: ServerResponse) => {
    const chunks: Buffer[] = []
    req.on('data', (c: Buffer) => chunks.push(c))
    req.on('end', () => {
      captured.push({ headers: req.headers, body: Buffer.concat(chunks) })
      res.writeHead(status).end()
    })
  })
  await new Promise<void>((resolve) => server.listen(0, '0.0.0.0', resolve))
  const port = (server.address() as { port: number }).port
  return {
    endpoint: `http://host.docker.internal:${port}/push`,
    captured,
    close: () => new Promise<void>((resolve) => server.close(() => resolve())),
  }
}

async function serverKey(request: APIRequestContext): Promise<string> {
  const res = await request.get('/api/push/key')
  expect(res.ok()).toBeTruthy()
  return (await res.json()).publicKey
}

interface Inject {
  endpoint: string
  p256dh: string
  auth: string
  appKey: string
  permission: 'default' | 'denied' | 'granted'
  existing: boolean
}

async function injectPush(page: Page, opts: Inject) {
  await page.addInitScript((o: Inject) => {
    const bytes = (s: string) => Uint8Array.from(
      atob(s.replace(/-/g, '+').replace(/_/g, '/') + '='.repeat((4 - (s.length % 4)) % 4)),
      (c) => c.charCodeAt(0),
    )
    let permission: NotificationPermission = o.permission
    Object.defineProperty(Notification, 'permission', { configurable: true, get: () => permission })
    Notification.requestPermission = async () => {
      permission = 'granted'
      return permission
    }
    const shape = (applicationServerKey: unknown) => {
      const json = () => ({ endpoint: o.endpoint, keys: { p256dh: o.p256dh, auth: o.auth } })
      return {
        endpoint: o.endpoint,
        options: { applicationServerKey },
        toJSON: json,
        unsubscribe: async () => true,
      }
    }
    let current: ReturnType<typeof shape> | null = o.existing ? shape(bytes(o.appKey).buffer) : null
    ;(window as unknown as { pushManager: unknown }).pushManager = {
      getSubscription: async () => current,
      subscribe: async (options: PushSubscriptionOptionsInit) => {
        current = shape(options.applicationServerKey)
        return current
      },
    }
  }, opts)
}

function compose(args: string[]) {
  execFileSync('docker', [
    'compose', '-f', '../docker-compose.yml', '-f', '../docker-compose.e2e.yml',
    '-p', COMPOSE_PROJECT, ...args,
  ], {
    stdio: 'pipe',
    env: {
      ...process.env,
      PCODER_E2E_DATA_DIR: `${process.cwd()}/${DATA_DIR}`,
      PCODER_E2E_API_PORT: String(API_PORT),
    },
  })
}

async function waitHealth() {
  await expect.poll(async () => {
    try {
      return (await fetch(`http://localhost:${API_PORT}/health`)).ok
    } catch {
      return false
    }
  }, { timeout: ciTimeout(90_000) }).toBe(true)
}

test.describe('push card', () => {
  test.use({ viewport: { width: 390, height: 844 }, hasTouch: true })

  test('prime, enable, and deliver a real encrypted push to the fake relay', async ({ page, request }) => {
    test.setTimeout(ciTimeout(120_000))
    const relay = await startRelay(201)
    try {
      const keys = clientKeys()
      const key = await serverKey(request)
      await injectPush(page, {
        endpoint: relay.endpoint, p256dh: keys.p256dh, auth: keys.auth,
        appKey: key, permission: 'default', existing: false,
      })
      await page.goto('/app')

      await expect(page.getByTestId('push-enable')).toBeVisible()
      await expect(page.getByTestId('push-card')).toHaveScreenshot('push-prime.png')

      await page.getByTestId('push-enable').click()
      await expect(page.getByTestId('push-success')).toBeVisible()
      await expect(page.getByTestId('push-success')).toHaveScreenshot('push-success.png')
      await page.getByTestId('push-success-close').click()
      await expect(page.getByTestId('push-test')).toBeVisible()
      await expect(page.getByTestId('push-devices')).toBeVisible()

      await expect.poll(() => relay.captured.length, { timeout: ciTimeout(20_000) }).toBe(1)
      const sent = relay.captured[0]
      expect(String(sent.headers.authorization)).toMatch(/^vapid /)
      expect(sent.headers['content-encoding']).toBe('aes128gcm')
      expect(sent.body.length).toBeGreaterThan(100)

      await expect(page.getByTestId('push-card')).toHaveScreenshot('push-enabled.png', {
        mask: [page.getByTestId('push-devices')],
        maskColor: '#e5e7eb',
      })
    } finally {
      await relay.close()
    }
  })

  test('denied permission shows re-enable steps', async ({ page, request }) => {
    const keys = clientKeys()
    const key = await serverKey(request)
    await injectPush(page, {
      endpoint: 'http://host.docker.internal:1/push', p256dh: keys.p256dh, auth: keys.auth,
      appKey: key, permission: 'denied', existing: false,
    })
    await page.goto('/app')
    await expect(page.getByTestId('push-denied')).toBeVisible()
    await expect(page.getByTestId('push-enable')).toHaveCount(0)
    await expect(page.getByTestId('push-card')).toHaveScreenshot('push-denied.png')
  })

  test('a relay auth failure surfaces without claiming success', async ({ page, request }) => {
    const relay = await startRelay(401)
    try {
      const keys = clientKeys()
      const key = await serverKey(request)
      await injectPush(page, {
        endpoint: relay.endpoint, p256dh: keys.p256dh, auth: keys.auth,
        appKey: key, permission: 'default', existing: false,
      })
      await page.goto('/app')
      await page.getByTestId('push-enable').click()
      await expect(page.getByTestId('push-error')).toContainText('VAPID')
      await expect(page.getByTestId('push-enable')).toBeVisible()
    } finally {
      await relay.close()
    }
  })

  test('rejects foreign navigate and untrusted endpoints', async ({ request }) => {
    const keys = clientKeys()
    const send = await request.post('/api/push', {
      data: { title: 't', body: 'b', navigate: 'https://evil.example/app' },
    })
    expect(send.status()).toBe(400)

    const endpoint = await request.post('/api/push/subscribe', {
      data: { endpoint: 'https://evil.example/push', keys: { p256dh: keys.p256dh, auth: keys.auth } },
    })
    expect(endpoint.status()).toBe(400)
  })

  test('deleting vapid.json rotates keys and requires Enable again', async ({ page, request }) => {
    test.setTimeout(ciTimeout(180_000))
    const relay = await startRelay(201)
    try {
      const keys = clientKeys()
      const oldKey = await serverKey(request)
      await injectPush(page, {
        endpoint: relay.endpoint, p256dh: keys.p256dh, auth: keys.auth,
        appKey: oldKey, permission: 'granted', existing: true,
      })
      await page.goto('/app')
      await expect(page.getByTestId('push-enable')).toBeVisible()
      await page.getByTestId('push-enable').click()
      await expect(page.getByTestId('push-success')).toBeVisible()
      await page.getByTestId('push-success-close').click()
      await expect(page.getByTestId('push-test')).toBeVisible()
      const sentBeforeRotation = relay.captured.length

      compose(['exec', '-T', 'server', 'rm', '-f', '/data/vapid.json'])
      compose(['restart', 'server'])
      await waitHealth()

      const newKey = await serverKey(request)
      expect(newKey).not.toBe(oldKey)

      // Rotation clears the server store. Resync must not rebuild it without
      // a gesture: the card shows Enable despite the browser still holding
      // the subscription minted against the old key.
      await page.reload()
      await expect(page.getByTestId('push-enable')).toBeVisible()
      await page.getByTestId('push-enable').click()
      await expect(page.getByTestId('push-success')).toBeVisible()
      await page.getByTestId('push-success-close').click()
      await expect(page.getByTestId('push-test')).toBeVisible()

      const list = await request.get('/api/push/subscriptions')
      const body = await list.json()
      expect(body.subscriptions.map((s: { endpoint: string }) => s.endpoint)).toContain(relay.endpoint)

      // Enable fires its own test push through the rotated key.
      await expect.poll(() => relay.captured.length, { timeout: ciTimeout(20_000) }).toBe(sentBeforeRotation + 1)
    } finally {
      await relay.close()
    }
  })
})
