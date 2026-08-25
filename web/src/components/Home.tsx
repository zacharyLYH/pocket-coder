import { useEffect, useState } from 'react'

import { Button } from '@/components/ui/button'
import { Card, CardHeader, CardTitle } from '@/components/ui/card'
import { HarnessesCard } from '@/components/HarnessesCard'
import { ProjectsCard } from '@/components/ProjectsCard'
import { SshKeysCard } from '@/components/SshKeysCard'
import { api } from '@/lib/api'
import type { SSHKey } from '@/lib/types'
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

  function loadSshKeys() {
    api<{ keys: SSHKey[] }>('/api/ssh-keys')
      .then((data) => setSshKeys(data.keys))
      .catch(() => {})
  }

  useEffect(() => { loadSshKeys() }, [])

  async function logout() {
    try {
      await fetch('/api/auth/logout', { method: 'POST' })
    } catch {
      // still sign out client-side
    }
    onLogout()
  }

  return (
    <main className="mx-auto w-full max-w-md p-4">
      <header className="flex items-center justify-between py-4">
        <h1 className="text-lg font-semibold">Side Project Saviour</h1>
        <Button variant="ghost" size="sm" onClick={logout}>
          Log out
        </Button>
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
          navigate={navigate}
        />
      </div>
      <HarnessesCard projects={projects} />
      <SshKeysCard keys={sshKeys} onChanged={loadSshKeys} />
    </main>
  )
}
