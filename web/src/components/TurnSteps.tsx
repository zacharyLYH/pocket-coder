import { useState } from 'react'
import { ChevronDown, Wrench } from 'lucide-react'
import type { AgentStep } from '@/lib/types'

export function TurnSteps({ steps, testPrefix }: { steps: AgentStep[]; testPrefix: string }) {
  const [open, setOpen] = useState(false)
  if (steps.length === 0) return null
  return (
    <div className="mt-2 overflow-hidden rounded-xl border bg-muted/40">
      <button
        className="flex min-h-[36px] w-full items-center gap-1.5 px-3 py-1.5 text-left text-xs text-muted-foreground"
        onClick={() => setOpen((v) => !v)}
        aria-expanded={open}
        data-testid={`${testPrefix}-steps-toggle`}
      >
        <Wrench className="size-3.5" />
        {steps.length} step{steps.length === 1 ? '' : 's'}
        <ChevronDown className={`size-3.5 transition-transform ${open ? 'rotate-180' : ''}`} />
      </button>
      {open && (
        <div className="flex flex-col gap-1 border-t p-2" data-testid={`${testPrefix}-steps`}>
          {steps.map((s, i) => (
            <div key={i} className="rounded-lg bg-background/80 px-2 py-1.5">
              <p className="font-mono text-[11px] font-medium">{s.tool}</p>
              {s.args && <p className="mt-0.5 font-mono text-[11px] break-all text-muted-foreground">{s.args}</p>}
              {(s.output || s.error) && (
                <pre className="mt-1 max-h-40 overflow-y-auto rounded-md bg-muted/60 p-1.5 font-mono text-[11px] whitespace-pre-wrap text-muted-foreground">
                  {s.error ? `error: ${s.error}` : s.output}
                </pre>
              )}
            </div>
          ))}
        </div>
      )}
    </div>
  )
}
