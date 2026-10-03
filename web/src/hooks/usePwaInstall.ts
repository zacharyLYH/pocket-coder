import { useCallback, useEffect, useState } from 'react'

// The install prompt event. Fires at most once per page load, possibly
// before React mounts — so index.html stashes it on window and the hook
// consumes the stash plus its own listener for later fires.
export interface PwaInstallEvent extends Event {
  prompt: () => Promise<void>
  userChoice: Promise<{ outcome: 'accepted' | 'dismissed' }>
}

declare global {
  interface Window {
    __pwaInstallEvent?: PwaInstallEvent
  }
}

function runningStandalone(): boolean {
  if (window.matchMedia?.('(display-mode: standalone)').matches) return true
  return (navigator as { standalone?: boolean }).standalone === true
}

export function isPwaStandalone(): boolean {
  return runningStandalone()
}

export function usePwaInstall() {
  const [deferred, setDeferred] = useState<PwaInstallEvent | null>(null)
  const [installed, setInstalled] = useState(() => runningStandalone())

  useEffect(() => {
    if (runningStandalone()) {
      setInstalled(true)
      return
    }
    // Take a prompt that fired before mount (the index.html stash).
    if (window.__pwaInstallEvent) {
      setDeferred(window.__pwaInstallEvent)
      window.__pwaInstallEvent = undefined
    }
    const onPrompt = (e: Event) => {
      e.preventDefault()
      setDeferred(e as PwaInstallEvent)
    }
    const onInstalled = () => {
      setInstalled(true)
      setDeferred(null)
    }
    window.addEventListener('beforeinstallprompt', onPrompt)
    window.addEventListener('appinstalled', onInstalled)
    return () => {
      window.removeEventListener('beforeinstallprompt', onPrompt)
      window.removeEventListener('appinstalled', onInstalled)
    }
  }, [])

  const install = useCallback(async () => {
    if (!deferred) return false
    await deferred.prompt()
    const choice = await deferred.userChoice
    setDeferred(null)
    if (choice.outcome === 'accepted') setInstalled(true)
    return choice.outcome === 'accepted'
  }, [deferred])

  return { canInstall: deferred !== null, installed, install }
}
