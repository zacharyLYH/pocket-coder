// Pocket Coder service worker (v1: installability only).
//
// The fetch handler exists so the app is installable; v1 deliberately
// caches nothing — the shell is always network (self-hosted servers
// deploy fast, stale shells are worse than offline), and /api + /ws
// always bypass. Push lands here later (push/subscription handlers +
// notificationclick) with no client rework: updates propagate to
// installed clients via the normal SW update flow.
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
  // v1: no runtime caching — pass through to network.
})
