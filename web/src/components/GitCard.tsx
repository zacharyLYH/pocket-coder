import { useEffect, useState } from 'react'
import { Copy, ExternalLink, FlaskConical, RefreshCw } from 'lucide-react'

import { Button } from '@/components/ui/button'
import { AlertDialog, AlertDialogAction, AlertDialogCancel, AlertDialogContent, AlertDialogDescription, AlertDialogFooter, AlertDialogHeader, AlertDialogTitle } from '@/components/ui/alert-dialog'
import { api, errMsg, probeErr, probeSignal } from '@/lib/api'
import { copyToClipboard } from '@/lib/clipboard'
import { GITHUB_KEYS_URL } from '@/lib/site'

type ServerKey = { publicKey: string; fingerprint: string; createdAt: string }

// GitCard: copy the server key to GitHub, then test. The private half
// never renders. onTestSuccess unlocks the parent on a passed probe;
// onChanged re-probes it after a regenerate.
export function GitCard({ onChanged, onTestSuccess, showRegenerate = true }: {
  onChanged?: () => void
  onTestSuccess?: () => void
  showRegenerate?: boolean
}) {
  const [key, setKey] = useState<ServerKey | null>(null)
  const [testedUser, setTestedUser] = useState<string | null>(null)
  const [error, setError] = useState<string | null>(null)
  const [busy, setBusy] = useState<'test' | 'regen' | null>(null)
  const [copied, setCopied] = useState(false)
  const [confirmRegen, setConfirmRegen] = useState(false)
  const [loading, setLoading] = useState(true)

  // The server mints the key at boot but listens late, so retry the first
  // load instead of sticking on "loading…" until a manual refresh.
  async function load(): Promise<void> {
    setLoading(true)
    for (let i = 0; i < 6; i++) {
      try {
        setKey(await api<ServerKey>('/api/ssh'))
        setError(null)
        break
      } catch (err) {
        if (i === 5) setError(`Could not load the server key (${errMsg(err)}). The server may still be starting.`)
        else await new Promise((r) => setTimeout(r, 1000 * (i + 1)))
      }
    }
    setLoading(false)
  }
  useEffect(() => { void load() }, [])

  async function test() {
    setBusy('test')
    setError(null)
    setTestedUser(null)
    try {
      // POST: the probe dials GitHub and logs, so it must not be cached.
      const d = await api<{ ok: boolean; user: string }>('/api/ssh/test', { method: 'POST', signal: probeSignal() })
      setTestedUser(d.user || 'ok')
      onTestSuccess?.()
    } catch (err) {
      setError(probeErr(err))
    } finally {
      setBusy(null)
    }
  }

  async function regen() {
    setConfirmRegen(false)
    setBusy('regen')
    setError(null)
    setTestedUser(null)
    try {
      const d = await api<ServerKey>('/api/ssh/regenerate', { method: 'POST' })
      setKey(d)
      onChanged?.()
    } catch (err) {
      setError(errMsg(err))
    } finally {
      setBusy(null)
    }
  }

  async function copy() {
    if (!key) return
    if (await copyToClipboard(key.publicKey)) {
      setCopied(true)
      setTimeout(() => setCopied(false), 2000)
    } else {
      setError('Copy failed — select the key text manually.')
    }
  }

  return (
    <div data-testid="git-card" className="flex flex-col gap-3">

      {showRegenerate && (
        <p className="text-xs text-muted-foreground">
          Pocket Coder uses this deploy key to clone repos over SSH and authenticate every git operation from your project containers.
        </p>
      )}

      <div className="flex flex-col gap-2">
        <p className="text-xs font-medium">1. Copy the public key</p>
        <div
          data-testid="git-public-key"
          className="max-h-24 overflow-auto rounded-md bg-muted p-2 font-mono text-xs break-all"
        >
          {key?.publicKey ?? (loading ? 'loading…' : 'failed to load')}
        </div>
        <div className="flex gap-2">
          <Button variant="outline" size="sm" className="self-start min-h-[44px]" disabled={!key} onClick={() => void copy()} data-testid="git-copy">
            <Copy className="size-4" />{copied ? 'Copied' : 'Copy'}
          </Button>
          {!key && !loading && (
            <Button variant="outline" size="sm" className="self-start min-h-[44px]" onClick={() => { setError(null); void load() }} data-testid="git-retry">
              <RefreshCw className="size-4" />Retry
            </Button>
          )}
        </div>
      </div>

      <div className="flex flex-col gap-2">
        <div className="flex items-center justify-between gap-2">
          <p className="text-xs font-medium">2. Add to GitHub</p>
          <a
            href={GITHUB_KEYS_URL}
            target="_blank"
            rel="noopener noreferrer"
            className="inline-flex items-center gap-1 text-sm text-blue-600 underline underline-offset-4"
            data-testid="github-keys-link"
          >
            <ExternalLink className="size-3" />Open GitHub SSH and GPG keys
          </a>
        </div>
      </div>

      <div className="flex flex-col gap-2">
        <p className="text-xs font-medium">3. Test the connection</p>
        <Button variant="outline" size="sm" className="min-h-[44px] w-full" disabled={busy !== null || !key} onClick={() => void test()} data-testid="git-test">
          <FlaskConical className="size-4" />{busy === 'test' ? 'Testing…' : 'Test connection'}
        </Button>
        {!showRegenerate && !testedUser && (
          <p className="text-xs text-muted-foreground">A passing test takes you to your projects.</p>
        )}
      </div>

      {testedUser && (
        <p className="text-xs text-muted-foreground" data-testid="git-tested">Authenticated as {testedUser}</p>
      )}
      {error && (
        <p className="text-destructive max-h-24 overflow-auto break-all text-xs" data-testid="git-error">{error}</p>
      )}

      {showRegenerate && key && (
        <Button variant="ghost" className="min-h-[44px] text-destructive self-start" disabled={busy !== null} onClick={() => setConfirmRegen(true)} data-testid="git-regen">
          <RefreshCw className="size-4" />{busy === 'regen' ? 'Regenerating...' : 'Regenerate key'}
        </Button>
      )}

      <AlertDialog open={confirmRegen} onOpenChange={(o) => { if (!o) setConfirmRegen(false) }}>
        <AlertDialogContent>
          <AlertDialogHeader>
            <AlertDialogTitle>Regenerate server key?</AlertDialogTitle>
            <AlertDialogDescription>
              The old public key stops working everywhere it was installed — remove it from GitHub after adding the new one. Existing clones keep working until they need the network. This cannot be undone.
            </AlertDialogDescription>
          </AlertDialogHeader>
          <AlertDialogFooter>
            <AlertDialogCancel>Cancel</AlertDialogCancel>
            <AlertDialogAction onClick={() => { void regen() }} data-testid="git-confirm-regen">Regenerate</AlertDialogAction>
          </AlertDialogFooter>
        </AlertDialogContent>
      </AlertDialog>

      {key?.fingerprint && <p data-testid="git-fingerprint" className="font-mono text-xs text-muted-foreground break-all">{key.fingerprint}</p>}
    </div>
  )
}
