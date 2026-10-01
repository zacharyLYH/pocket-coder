import {
  Activity,
  Bot,
  Command,
  GitBranch,
  Globe,
  Layers,
  Map,
  Terminal,
  Zap,
} from 'lucide-react'

// LandingTooling: double-row logo-cloud marquee under the hero. Two rows
// scroll in opposite directions (classic SaaS "trusted by" strip), edge
// faded, pausing on hover. Each tile mirrors a real surface in the app.
// Split so neither row repeats itself adjacently and both halves of each
// track are identical (the -50% loop point stays seamless).
const ROW_ONE = [
  {
    icon: Terminal,
    name: 'Terminal',
    desc: 'Real tmux sessions on your phone',
  },
  {
    icon: Command,
    name: 'Coding CLIs',
    desc: 'opencode, codex, claude + yours',
  },
  {
    icon: Bot,
    name: 'Butler',
    desc: 'Ops copilot for the fleet',
  },
  {
    icon: Map,
    name: 'Codemaps',
    desc: 'AI reader that explains repos',
  },
  {
    icon: Globe,
    name: 'Preview',
    desc: 'Localhost ports in a browser',
  },
]

const ROW_TWO = [
  {
    icon: GitBranch,
    name: 'Git review',
    desc: 'Stage, commit, push, PR drafts',
  },
  {
    icon: Activity,
    name: 'Observability',
    desc: 'Metrics, logs, error groups',
  },
  {
    icon: Layers,
    name: 'Fleet',
    desc: 'Many projects, run everywhere',
  },
  {
    icon: Zap,
    name: 'Shortcuts',
    desc: 'One-tap commands + keys',
  },
]

type Feature = { icon: typeof Terminal; name: string; desc: string }

function MarqueeRow({ items, reverse, testid }: { items: Feature[]; reverse?: boolean; testid: string }) {
  return (
    <div className="tooling-viewport -mx-4 overflow-hidden px-4" data-testid={testid}>
      <div className={`tooling-track ${reverse ? 'tooling-track-reverse' : ''}`}>
        {[0, 1].map((half) => (
          // Two identical halves: the -50% loop point lands exactly on the
          // seam. The trailing pr-3 stands in for the inter-tile gap so the
          // seam spacing matches. Second half is decorative duplication.
          <div key={half} aria-hidden={half === 1} className="flex shrink-0 gap-3 pr-3">
            {items.map((f) => (
              <div
                key={f.name}
                className="flex w-60 shrink-0 flex-col gap-2 rounded-xl border border-border bg-card p-4"
              >
                <span className="flex size-8 items-center justify-center rounded-md border border-border bg-muted">
                  <f.icon className="size-4 text-foreground" />
                </span>
                <span className="text-sm font-medium tracking-tight">{f.name}</span>
                <span className="text-xs leading-snug text-muted-foreground">{f.desc}</span>
              </div>
            ))}
          </div>
        ))}
      </div>
    </div>
  )
}

export function LandingTooling() {
  return (
    <section aria-label="What is inside" className="tooling-marquee flex flex-col gap-3">
      <p className="text-xs font-medium uppercase tracking-widest text-muted-foreground">
        Everything in the box
      </p>
      <div className="flex flex-col gap-3" data-testid="tooling-cloud">
        <MarqueeRow items={ROW_ONE} testid="tooling-row-one" />
        <MarqueeRow items={ROW_TWO} reverse testid="tooling-row-two" />
      </div>
    </section>
  )
}
