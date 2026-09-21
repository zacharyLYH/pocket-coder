import { useEffect, useState } from 'react'

import { Button } from '@/components/ui/button'
import { Card, CardHeader, CardTitle } from '@/components/ui/card'
import { BusyOverlay } from '@/components/BusyOverlay'
import { AICard } from '@/components/AICard'
import { GitCard } from '@/components/GitCard'
import { HarnessesCard } from '@/components/HarnessesCard'
import { ProjectsCard } from '@/components/ProjectsCard'
import { SshKeysCard } from '@/components/SshKeysCard'
import { ThemeToggle } from '@/components/ThemeToggle'
import { api } from '@/lib/api'
import type { GitConfigStatus, SSHKey } from '@/lib/types'
import { useProjects } from '@/hooks/useProjects'

// The logged-in home page: projects, the harness/command orchestration
// card, and SSH keys. Owns the shared state both cards need — the project
// list (harness pickers render from it) and the SSH key list (the clone
// hint in the project form counts it).
export function Home({ email, onLogout, navigate }: {
  email: string
  onLogout: () => void
  navigate: (to: string) => void
}) {
  const { projects, loading, error, refresh } = useProjects()
  const [sshKeys, setSshKeys] = useState<SSHKey[]>([])
  const [harnessBusy, setHarnessBusy] = useState(false)
  const [gitConfigured, setGitConfigured] = useState(true)

  function loadSshKeys() {
    api<{ keys: SSHKey[] }>('/api/ssh-keys')
      .then((data) => setSshKeys(data.keys))
      .catch(() => {})
  }

  function loadGit() {
    api<GitConfigStatus>('/api/git/config')
      .then((d) => setGitConfigured(d.configured))
      .catch(() => {})
  }

  useEffect(() => { loadSshKeys() }, [])
  useEffect(() => { loadGit() }, [])

  async function logout() {
    try {
      await api('/api/auth/logout', { method: 'POST' })
    } catch {
      // still sign out client-side
    }
    onLogout()
  }

  return (
    <>
      <main className="mx-auto w-full max-w-md p-4">
        <header className="flex items-center justify-between py-4">
          <h1 className="text-lg font-semibold">Pocket Coder</h1>
          <div className="flex items-center gap-1">
            <ThemeToggle />
            <Button variant="ghost" size="sm" onClick={logout}>
              Log out
            </Button>
          </div>
        </header>
        <Card>
          <CardHeader>
            <CardTitle className="text-base">Welcome, {email}</CardTitle>
          </CardHeader>
        </Card>
        <div className="mt-4">
          <ProjectsCard
            projects={projects}
            loading={loading}
            error={error}
            refresh={refresh}
            sshKeyCount={sshKeys.length}
            gitConfigured={gitConfigured}
            navigate={navigate}
          />
        </div>
        <HarnessesCard projects={projects} onInstalled={refresh} onBusyChange={setHarnessBusy} />
        <AICard />
        <GitCard onChanged={loadGit} />
        <SshKeysCard keys={sshKeys} onChanged={loadSshKeys} />
      </main>
      {harnessBusy && <BusyOverlay />}
    </>
  )
}
