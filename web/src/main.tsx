import { StrictMode } from 'react'
import { createRoot } from 'react-dom/client'
import './index.css'
import App from './App'

createRoot(document.getElementById('root')!).render(
  <StrictMode>
    <App />
  </StrictMode>,
)

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
