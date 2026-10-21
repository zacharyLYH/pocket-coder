// Web push client plumbing. Two surfaces exist in the wild: Safari's
// window.pushManager (declarative push) and the standard PushManager on a
// service worker registration. Everything below hides that fork.
import { api } from './api'

export interface PushKeys {
  p256dh: string
  auth: string
}

export interface PushSubscriptionJSON {
  endpoint: string
  keys: PushKeys
}

export interface StoredSubscription {
  endpoint: string
  createdAt: string
}

interface SubscriptionLike {
  options: { applicationServerKey?: ArrayBuffer | null }
  toJSON(): PushSubscriptionJSON
  unsubscribe(): Promise<boolean>
}

interface ManagerLike {
  getSubscription(): Promise<SubscriptionLike | null>
  subscribe(options: PushSubscriptionOptionsInit): Promise<SubscriptionLike>
}

type PushWindow = Window & { pushManager?: ManagerLike }

export function isSupported(): boolean {
  return typeof window !== 'undefined' && 'Notification' in window &&
    ('serviceWorker' in navigator || 'pushManager' in window)
}

// resolveClickTarget accepts only the app's own origin. The service worker
// and the notification click path both rely on this rule.
export function resolveClickTarget(navigate: string, origin: string): string | null {
  try {
    const url = new URL(navigate, origin)
    return url.origin === origin ? url.href : null
  } catch {
    return null
  }
}

export function keyToBase64(key?: ArrayBuffer | ArrayBufferView | null): string {
  if (!key) return ''
  const bytes = key instanceof ArrayBuffer ? new Uint8Array(key) : new Uint8Array(key.buffer, key.byteOffset, key.byteLength)
  let raw = ''
  for (const b of bytes) raw += String.fromCharCode(b)
  return btoa(raw).replace(/\+/g, '-').replace(/\//g, '_').replace(/=+$/, '')
}

export function decodeKey(base64: string): Uint8Array<ArrayBuffer> {
  const padded = (base64 + '='.repeat((4 - (base64.length % 4)) % 4))
    .replace(/-/g, '+').replace(/_/g, '/')
  return Uint8Array.from(atob(padded), (c) => c.charCodeAt(0))
}

function withTimeout<T>(promise: Promise<T>, ms: number, message: string): Promise<T> {
  return new Promise((resolve, reject) => {
    const timer = setTimeout(() => reject(new Error(message)), ms)
    promise.then(
      (value) => { clearTimeout(timer); resolve(value) },
      (err) => { clearTimeout(timer); reject(err) },
    )
  })
}

// manager registers the worker on demand. Dev mode does not register it in
// main.tsx (production only), so waiting on ready alone would hang forever.
async function manager(): Promise<ManagerLike> {
  const own = (window as PushWindow).pushManager
  if (own) return own
  if (!('serviceWorker' in navigator)) throw new Error('Push is not supported in this browser.')
  const reg = await withTimeout((async () => {
    const existing = await navigator.serviceWorker.getRegistration()
    if (!existing) await navigator.serviceWorker.register('/sw.js')
    return navigator.serviceWorker.ready
  })(), 15_000, 'The service worker did not start. Reload the page and try again.')
  return reg.pushManager as unknown as ManagerLike
}

async function hasManager(): Promise<boolean> {
  if ((window as PushWindow).pushManager) return true
  if (!('serviceWorker' in navigator)) return false
  return Boolean(await navigator.serviceWorker.getRegistration())
}

async function publicKey(): Promise<string> {
  const { publicKey } = await api<{ publicKey: string }>('/api/push/key')
  return publicKey
}

async function store(sub: PushSubscriptionJSON): Promise<void> {
  await api('/api/push/subscribe', { method: 'POST', body: JSON.stringify(sub) })
}

// subscribe reuses the browser subscription unless it was minted against a
// different application server key (the vapid.json rotation path).
async function subscribe(key: string): Promise<PushSubscriptionJSON> {
  const pm = await manager()
  const existing = await pm.getSubscription()
  if (existing) {
    if (keyToBase64(existing.options.applicationServerKey) === key) return existing.toJSON()
    await existing.unsubscribe()
  }
  const sub = await pm.subscribe({ userVisibleOnly: true, applicationServerKey: decodeKey(key) })
  return sub.toJSON()
}

export type PushState = 'enabled' | 'default' | 'denied' | 'unsupported'

// resync only inspects. It never subscribes, unsubscribes, or POSTs: every
// registration and repair goes through Enable so a wiped server store cannot
// be repopulated without a user gesture (start-docker relies on this).
export async function resync(): Promise<PushState> {
  if (!isSupported()) return 'unsupported'
  if (Notification.permission === 'denied') return 'denied'
  if (Notification.permission !== 'granted') return 'default'
  if (!(await hasManager())) return 'default'
  const pm = await manager()
  const existing = await pm.getSubscription()
  if (!existing) return 'default'
  const key = await publicKey()
  if (keyToBase64(existing.options.applicationServerKey) !== key) return 'default'
  const endpoint = existing.toJSON().endpoint
  return (await listSubscriptions()).some((s) => s.endpoint === endpoint)
    ? 'enabled'
    : 'default'
}

// enable must run inside a user gesture: iOS rejects requestPermission
// otherwise.
export async function enable(): Promise<void> {
  if (!isSupported()) throw new Error('Push is not supported in this browser.')
  const permission = await Notification.requestPermission()
  if (permission !== 'granted') {
    throw new Error(permission === 'denied'
      ? 'Notifications are blocked. Re-enable them in your device settings.'
      : 'Notification permission was not granted.')
  }
  await store(await subscribe(await publicKey()))
  await sendTest()
}

export async function sendTest(): Promise<void> {
  const res = await api<{ sent: number; failed: number; pruned: number }>('/api/push', {
    method: 'POST',
    body: JSON.stringify({
      title: 'Pocket Coder',
      body: "Notifications on. You'll hear from us when your agent needs you.",
    }),
  })
  if (res.sent === 0) {
    throw new Error('The push service did not accept the test notification. Try again.')
  }
}

export async function listSubscriptions(): Promise<StoredSubscription[]> {
  const { subscriptions } = await api<{ subscriptions: StoredSubscription[] }>('/api/push/subscriptions')
  return subscriptions
}
