import { useEffect, useState } from 'react'
import { Star } from 'lucide-react'

import { Button } from '@/components/ui/button'
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from '@/components/ui/card'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'
import { LandingTooling } from '@/components/LandingTooling'
import {
  BIO_URL,
  GITHUB_KEYS_URL,
  GITHUB_URL,
  GMAIL_GUIDE_URL,
  LINKEDIN_URL,
  buildSetupCommand,
} from '@/lib/site'

// Landing page at /. One motivation, one action, install docs for
// self-hosters, the after-login guide, socials buried in the footer.
// Logged in, the calls to action open /app instead of /login.
export function LandingPage({ onLogin, loggedIn }: { onLogin: () => void; loggedIn?: boolean }) {
  const [domain, setDomain] = useState('')
  const host = cleanHost(domain)
  const loginUrl = host === '' ? 'http://your-box:8080/login' : `http://${host}:8080/login`
  return (
    <div className="min-h-dvh bg-background text-foreground">
      <header className="sticky top-0 z-10 border-b border-border/60 bg-background/80 backdrop-blur-xl">
        <div className="mx-auto flex w-full max-w-3xl items-center gap-3 px-4 py-3">
          <img src="/icons/icon-192.png" alt="Pocket Coder" className="size-8 rounded-md" />
          <span className="text-[17px] font-semibold tracking-tight">Pocket Coder</span>
          <span className="flex-1" />
          <StarButton />
          <Button size="sm" onClick={onLogin}>
            {loggedIn ? 'Open app' : 'Log in'}
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

        <section className="flex flex-col gap-4">
          <h2 className="text-2xl font-semibold tracking-tight">Prerequisites</h2>
          <Card>
            <CardContent className="text-sm text-muted-foreground">
              <ul className="flex list-disc flex-col gap-1 pl-5">
                <li>A domain you control</li>
                <li>A Linux box with at least 4 GB RAM and port 8080 open</li>
                <li>A Gmail address for the login PIN</li>
              </ul>
            </CardContent>
          </Card>
        </section>

        <section id="get-started" className="flex scroll-mt-20 flex-col gap-4">
          <h2 className="text-2xl font-semibold tracking-tight">Get started</h2>

          <Card>
            <CardHeader>
              <CardTitle className="text-base">1. Provision a domain and a box</CardTitle>
              <CardDescription>
                A domain plus any Linux box with at least 4 GB RAM and port
                8080 open.
              </CardDescription>
            </CardHeader>
          </Card>

          <Card>
            <CardHeader>
              <CardTitle className="text-base">2. Get a Gmail app password</CardTitle>
              <CardDescription>
                Pocket Coder emails you a login PIN, so it needs SMTP first.
              </CardDescription>
            </CardHeader>
            <CardContent>
              <Button variant="outline" asChild>
                <a href={GMAIL_GUIDE_URL} className='underline text-blue-500' target="_blank" rel="noreferrer">
                  Open the Gmail app-password guide
                </a>
              </Button>
            </CardContent>
          </Card>

          <SetupBuilder />

          <StepDomain domain={domain} host={host} onDomain={setDomain} />

          <Card>
            <CardHeader>
              <CardTitle className="text-base">5. Log in</CardTitle>
              <CardDescription>
                {host === '' ? (
                  <>Open http://your-box:8080/login, enter your email, and type the PIN that arrives by inbox.</>
                ) : (
                  <>Open <a href={loginUrl} target="_blank" rel="noreferrer" className="underline text-blue-500">{loginUrl}</a>, enter your email, and type the PIN that arrives by inbox.</>
                )}
              </CardDescription>
            </CardHeader>
          </Card>

          <Card>
            <CardHeader>
              <CardTitle className="text-base">6. Connect GitHub</CardTitle>
              <CardDescription>
                Copy the server key, add it on GitHub, then test the
                connection. This is what the app shows you.
              </CardDescription>
            </CardHeader>
            <CardContent className="flex flex-col gap-3">
              <blockquote className="border-l-2 border-primary pl-3 text-sm font-medium">
                This step cannot be skipped. Cloning only works after the
                deploy key is on GitHub.
              </blockquote>
              <img
                src="/onboarding/ssh-card.png"
                alt="Connect GitHub first card: copy the key, add it on GitHub, test the connection"
                className="w-full rounded-md border border-border"
                loading="lazy"
              />
              <Button variant="outline" asChild className="self-start">
                <a href={GITHUB_KEYS_URL} target="_blank" rel="noreferrer">
                  Open GitHub SSH and GPG keys
                </a>
              </Button>
            </CardContent>
          </Card>

          <Card>
            <CardHeader>
              <CardTitle className="text-base">7. Clone a repo</CardTitle>
              <CardDescription>
                Paste any clone URL, HTTPS or SSH, and press Clone project.
              </CardDescription>
            </CardHeader>
            <CardContent className="flex flex-col gap-3">
              <img
                src="/onboarding/clone-card.png"
                alt="Projects card: paste a clone URL and press Clone project"
                className="w-full rounded-md border border-border"
                loading="lazy"
              />
            </CardContent>
          </Card>

          <Card>
            <CardHeader>
              <CardTitle className="text-base">8. Use a desktop for setup</CardTitle>
              <CardDescription>
                Copying keys and navigating GitHub settings is easier with a
                desktop clipboard and screen. Once set up, use the app freely
                from mobile.
              </CardDescription>
            </CardHeader>
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

// StarButton links to the repo and shows the live count. The count comes
// from the public GitHub API, no key needed. If the fetch fails the link
// still works, it just reads "Star".
function StarButton() {
  const [stars, setStars] = useState<number | null>(null)
  useEffect(() => {
    fetch(`https://api.github.com/repos/${GITHUB_URL.split('github.com/')[1]}`)
      .then((r) => (r.ok ? r.json() : null))
      .then((d: { stargazers_count?: number } | null) => {
        if (typeof d?.stargazers_count === 'number') setStars(d.stargazers_count)
      })
      .catch(() => {})
  }, [])
  return (
    <Button variant="outline" size="sm" asChild>
      <a href={GITHUB_URL} target="_blank" rel="noreferrer" aria-label="Star Pocket Coder on GitHub">
        <Star className="size-4" />
        {stars === null ? 'Star' : stars.toLocaleString()}
      </a>
    </Button>
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
        <CardTitle className="text-base">3. Generate the command, then copy it</CardTitle>
        <CardDescription>
          Type below and the installer command fills itself in.
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
        <p className="text-sm text-muted-foreground">
          Tip: from the box itself, <Code>curl http://localhost:8080/health</Code> should
          answer <Code>{"{\"status\":\"ok\"}"}</Code>.
        </p>
      </CardContent>
    </Card>
  )
}

// cleanHost trims a pasted domain down to its host: no protocol, no path.
function cleanHost(raw: string): string {
  return raw.trim().replace(/^https?:\/\//i, '').replace(/\/.*$/, '')
}

// StepDomain asks for the domain the user linked, so the open link below
// points at their app. Nothing leaves the browser.
function StepDomain({ domain, host, onDomain }: { domain: string; host: string; onDomain: (d: string) => void }) {
  return (
    <Card>
      <CardHeader>
        <CardTitle className="text-base">4. Paste it on your box and link your domain</CardTitle>
        <CardDescription>
          Paste the command on the box as root and wait for the live
          banner. Then point your domain at the box so it reaches the
          running app on port 8080.
        </CardDescription>
      </CardHeader>
      <CardContent className="flex flex-col gap-3">
        <div className="flex flex-col gap-1.5">
          <Label htmlFor="landing-domain">Your domain</Label>
          <Input
            id="landing-domain"
            type="text"
            autoComplete="off"
            placeholder="coder.example.com"
            value={domain}
            onChange={(e) => onDomain(e.target.value)}
          />
        </div>
        {host !== '' && (
          <Button variant="outline" asChild className="self-start">
            <a href={`http://${host}:8080`} target="_blank" rel="noreferrer">
              Open your app
            </a>
          </Button>
        )}
        <p className="text-sm text-muted-foreground">
          {host === '' ? (
            <>Check it from your machine too: <Code>curl http://your-domain:8080/health</Code> should answer <Code>{"{\"status\":\"ok\"}"}</Code>.</>
          ) : (
            <>Check it from your machine: <a href={`http://${host}:8080/health`} target="_blank" rel="noreferrer" className="underline text-blue-500">http://{host}:8080/health</a> should answer <Code>{"{\"status\":\"ok\"}"}</Code>.</>
          )}
        </p>
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
