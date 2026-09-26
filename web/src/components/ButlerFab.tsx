import { useState } from 'react'
import { Bot } from 'lucide-react'
import { Button } from '@/components/ui/button'
import { ButlerSheet } from '@/components/ButlerSheet'

// ButlerFab: one floating button + centered floating chat card on mobile
// (margins all around, tap outside to exit), floating chat window on
// desktop (old-messenger vibe). Same component on Home and in the project
// header; the project id rides as a clearable hint chip.
export function ButlerFab({ projectHint }: { projectHint?: string | null }) {
  const [open, setOpen] = useState(false)
  const [hintCleared, setHintCleared] = useState(false)
  const hint = projectHint && !hintCleared ? projectHint : null
  return (
    <>
      <Button
        onClick={() => { setOpen((o) => !o); setHintCleared(false) }}
        data-testid="butler-fab"
        aria-label="Open Butler"
        size="icon"
        className="fixed bottom-5 right-5 z-40 size-12 rounded-full shadow-lg"
      >
        <Bot className="size-5" />
      </Button>
      {open && (
        <div className="fixed inset-0 z-50" data-testid="butler-overlay">
          <div className="absolute inset-0 bg-black/40 backdrop-blur" onClick={() => setOpen(false)} data-testid="butler-backdrop" />
          <div className="absolute inset-x-4 top-16 bottom-16 overflow-hidden rounded-2xl border bg-background shadow-xl sm:inset-x-auto sm:top-auto sm:right-5 sm:bottom-24 sm:left-auto sm:h-[540px] sm:max-h-[calc(100dvh-8rem)] sm:w-[380px] sm:rounded-2xl sm:border sm:pointer-events-auto sm:shadow-2xl" role="dialog" aria-label="Butler">
            <ButlerSheet projectHint={hint} onClearHint={() => setHintCleared(true)} onClose={() => setOpen(false)} />
          </div>
        </div>
      )}
    </>
  )
}
