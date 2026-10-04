// EmptyState is the shared dashed-border empty block for tab panels
// (no changes, no repo, no ports). Keeps the icon/title/hint shape and
// testid in one place.
export function EmptyState({ testid, title, hint }: { testid: string; title: string; hint: string }) {
  return (
    <div className="grid place-items-center rounded-lg border border-dashed bg-muted/30 p-8 text-center" data-testid={testid}>
      <div className="flex flex-col items-center gap-2">
        <span className="text-2xl">○</span>
        <p className="text-sm font-medium">{title}</p>
        <p className="text-xs text-muted-foreground">{hint}</p>
      </div>
    </div>
  )
}
