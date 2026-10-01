import { useState } from 'react'

import { Button } from '@/components/ui/button'
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from '@/components/ui/card'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'
import { LandingTooling } from '@/components/LandingTooling'
import {
  BIO_URL,
  GITHUB_URL,
  GMAIL_GUIDE_URL,
  LINKEDIN_URL,
  buildSetupCommand,
} from '@/lib/site'

// Landing page at /. One motivation, one action, install docs for
// self-hosters, socials buried in the footer.
export function LandingPage({ onLogin }: { onLogin: () => void }) {
  return (
    <div className="min-h-dvh bg-background text-foreground">
      <header className="sticky top-0 z-10 border-b border-border/60 bg-background/80 backdrop-blur-xl">
        <div className="mx-auto flex w-full max-w-3xl items-center gap-3 px-4 py-3">
          <img src="/icons/icon-192.png" alt="Pocket Coder" className="size-8 rounded-md" />
          <span className="text-[17px] font-semibold tracking-tight">Pocket Coder</span>
          <span className="flex-1" />
          <Button size="sm" onClick={onLogin}>
            Log in
          </Button>
        </div>
      </header>

      <main className="mx-auto flex w-full max-w-3xl flex-col gap-12 px-4 pb-16 pt-10">
        <section className="flex flex-col items-start gap-4">
          <h1 className="max-w-xl text-4xl font-semibold leading-tight tracking-tight md:text-5xl">
            Familiar AI powered dev box, in your pocket.
          </h1>
          <p className="max-w-xl text-lg text-muted-foreground">
            Code on the move without sacrificing control and modern ergonomics. Like Devin and GH Codespaces but free. 
          </p>
        </section>

        <LandingTooling />

        <section id="get-started" className="flex scroll-mt-20 flex-col gap-4">
          <h2 className="text-2xl font-semibold tracking-tight">Get started</h2>
          <p className="text-muted-foreground">
            A Linux box with 2 GB RAM and an open port 8080. The setup script
            does the rest.
          </p>

          <Card>
            <CardHeader>
              <CardTitle className="text-base">1. Get a Gmail app password</CardTitle>
              <CardDescription>
                Pocket Coder emails you a login PIN, so it needs SMTP first.
              </CardDescription>
            </CardHeader>
            <CardContent>
              <Button variant="outline" asChild>
                <a href={GMAIL_GUIDE_URL} target="_blank" rel="noreferrer">
                  Open the Gmail app-password guide
                </a>
              </Button>
            </CardContent>
          </Card>

          <SetupBuilder />

          <Card>
            <CardHeader>
              <CardTitle className="text-base">3. Log in</CardTitle>
              <CardDescription>
                Open http://your-box:8080/login, enter your email, and type the PIN
                that arrives by inbox.
              </CardDescription>
            </CardHeader>
            <CardContent>
              <Button variant="outline" onClick={onLogin}>
                Go to login
              </Button>
            </CardContent>
          </Card>
        </section>

        <footer className="border-t pt-6 text-sm text-muted-foreground">
          <p>
            Built by Zachary Lee · <a className="underline underline-offset-4" href={LINKEDIN_URL}>LinkedIn</a> ·{' '}
            <a className="underline underline-offset-4" href={BIO_URL}>Bio</a> ·{' '}
            <a className="underline underline-offset-4" href={GITHUB_URL}>Source</a> (AGPL-3.0)
          </p>
        </footer>
      </main>
    </div>
  )
}

// SetupBuilder is pure client state: type your email and app password and
// it fills in the installer command. Nothing leaves the browser.
function SetupBuilder() {
  const [email, setEmail] = useState('')
  const [smtpPassword, setSmtpPassword] = useState('')
  const command = buildSetupCommand(email, smtpPassword)
  // Placeholders render a plausible-but-broken command: keep copy
  // disabled until both fields hold something real.

  return (
    <Card>
      <CardHeader>
        <CardTitle className="text-base">2. Run the installer</CardTitle>
        <CardDescription>
          As root on your box. Type below and the command fills itself in.
          Re-run it anytime to update.
        </CardDescription>
      </CardHeader>
      <CardContent className="flex flex-col gap-3">
        <div className="grid gap-3 md:grid-cols-2">
          <div className="flex flex-col gap-1.5">
            <Label htmlFor="landing-email">Gmail address</Label>
            <Input
              id="landing-email"
              type="email"
              autoComplete="email"
              placeholder="you@gmail.com"
              value={email}
              onChange={(e) => setEmail(e.target.value)}
            />
          </div>
          <div className="flex flex-col gap-1.5">
            <Label htmlFor="landing-smtp">App password</Label>
            <Input
              id="landing-smtp"
              type="password"
              autoComplete="off"
              placeholder="xxxx xxxx xxxx xxxx"
              value={smtpPassword}
              onChange={(e) => setSmtpPassword(e.target.value)}
            />
          </div>
        </div>
        <CopyBlock text={command} disabled={email.trim() === '' || smtpPassword === ''} />
        <div className="text-sm text-muted-foreground">
          <p className="font-medium text-foreground">After install, this is all you run:</p>
          <ul className="mt-1 flex list-disc flex-col gap-1 pl-5">
            <li>
              Update: <Code>cd /opt/pocket-coder/src {'&&'} docker compose up -d --build</Code>
            </li>
            <li>
              Health check: <Code>curl http://localhost:8080/health</Code>
            </li>
          </ul>
        </div>
      </CardContent>
    </Card>
  )
}

function Code({ children }: { children: React.ReactNode }) {
  return <code className="rounded bg-muted px-1.5 py-0.5 font-mono text-xs">{children}</code>
}

function CopyBlock({ text, disabled }: { text: string; disabled?: boolean }) {
  const [copied, setCopied] = useState(false)
  async function copy() {
    try {
      await navigator.clipboard.writeText(text)
      setCopied(true)
      setTimeout(() => setCopied(false), 1500)
    } catch {
      // clipboard blocked: user can still select the text by hand
    }
  }
  return (
    <div className="flex flex-col gap-2">
      <pre className="overflow-x-auto rounded-md bg-muted p-3 font-mono text-xs leading-relaxed">{text}</pre>
      <Button variant="outline" size="sm" className="self-start" onClick={copy} disabled={disabled}>
        {copied ? 'Copied' : 'Copy command'}
      </Button>
    </div>
  )
}
