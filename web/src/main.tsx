import { createRoot } from 'react-dom/client'
import './index.css'
import App from './App'

// No StrictMode wrapper, deliberately: in dev it double-invokes every
// mount effect, so every list fetch costs two network calls against the
// real backend (duplicate /harnesses, /sessions, /ai/models traffic on
// every page and dialog open). That doubling is React's own dev behavior —
// no component-level fix can suppress it — and it trained nobody: the
// redundant calls it surfaced were all StrictMode artifacts, never real
// bugs. Single-fire mount discipline is pinned instead by unit tests that
// count fetches per mount and per re-render (HarnessesCard, TerminalView).
createRoot(document.getElementById('root')!).render(<App />)

// Service worker: production only, so dev HMR never fights the cache.
// v1 is installability + update channel (no offline caching); push
// handlers slot into sw.js later without touching this registration.
if (import.meta.env.PROD && 'serviceWorker' in navigator) {
  window.addEventListener('load', () => {
    void navigator.serviceWorker.register('/sw.js').catch(() => {
      // SW unsupported or blocked — the app works fine without it.
    })
  })
}
