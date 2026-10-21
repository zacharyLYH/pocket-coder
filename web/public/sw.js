// Pocket Coder service worker: installability, plus the classic push
// fallback for browsers without declarative Web Push (Chrome Android).
// The fetch handler caches nothing — the shell is always network, and
// /api + /ws always bypass.
self.addEventListener('install', () => {
  self.skipWaiting()
})

self.addEventListener('activate', (event) => {
  event.waitUntil(clients.claim())
})

self.addEventListener('fetch', (event) => {
  const url = new URL(event.request.url)
  // API, websockets, and cross-origin: never touch, always network.
  if (url.origin !== self.location.origin) return
  if (url.pathname.startsWith('/api') || url.pathname.startsWith('/ws')) return
  // No runtime caching — pass through to network.
})

// Same-origin guard, mirrored by resolveClickTarget in lib/push.ts.
function clickTarget(navigate) {
  try {
    const url = new URL(navigate, self.location.origin)
    return url.origin === self.location.origin ? url.href : null
  } catch {
    return null
  }
}

self.addEventListener('push', (event) => {
  let data
  try {
    data = event.data?.json()
  } catch {
    return
  }
  const n = data?.notification
  if (!n || typeof n.title !== 'string' || typeof n.body !== 'string') return
  event.waitUntil(self.registration.showNotification(n.title, {
    body: n.body,
    tag: typeof n.tag === 'string' ? n.tag : undefined,
    data: { navigate: typeof n.navigate === 'string' ? n.navigate : self.location.origin + '/app' },
  }))
})

self.addEventListener('notificationclick', (event) => {
  event.notification.close()
  const target = clickTarget(event.notification.data?.navigate)
  if (!target) return
  event.waitUntil((async () => {
    const windows = await clients.matchAll({ type: 'window', includeUncontrolled: true })
    for (const client of windows) {
      if (client.url === target && 'focus' in client) return client.focus()
    }
    return clients.openWindow(target)
  })())
})

// Browser-initiated rotation. Server-side VAPID rotation is not repaired
// here; the card's read-only resync shows Enable and the user re-registers.
self.addEventListener('pushsubscriptionchange', (event) => {
  event.waitUntil((async () => {
    const res = await fetch('/api/push/key', { credentials: 'include' })
    if (!res.ok) return
    const { publicKey } = await res.json()
    const pad = '='.repeat((4 - (publicKey.length % 4)) % 4)
    const raw = atob((publicKey + pad).replace(/-/g, '+').replace(/_/g, '/'))
    const key = Uint8Array.from(raw, (c) => c.charCodeAt(0))
    const sub = event.newSubscription || await self.registration.pushManager.subscribe({
      userVisibleOnly: true,
      applicationServerKey: key,
    })
    await fetch('/api/push/subscribe', {
      method: 'POST',
      credentials: 'include',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify(sub.toJSON()),
    })
  })())
})
