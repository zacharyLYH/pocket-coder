import { useEffect, useMemo, useState } from 'react'
import { EllipsisVertical, Pencil, Plus, Search, Trash2 } from 'lucide-react'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { Dialog, DialogContent, DialogDescription, DialogHeader, DialogTitle } from '@/components/ui/dialog'
import { DropdownMenu, DropdownMenuContent, DropdownMenuItem, DropdownMenuTrigger } from '@/components/ui/dropdown-menu'
import { Skeleton } from '@/components/ui/skeleton'
import { api, errMsg, projectPath } from '@/lib/api'
import { useShortcuts } from '@/hooks/useShortcuts'
import { keydownToLabel, shortcutText, toServerRow, validateDraft, type Shortcut } from '@/lib/shortcuts'

// One "Shortcuts" modal: run everything in one tap, manage the list
// inline, changes save instantly. No cmd-vs-keys distinction in the UI —
// the value resolves on save (key combo like Ctrl-C sends raw input,
// anything else injects as a command). The keys field captures physical
// keypresses so ⌘C / Ctrl-C / Esc register directly.
export function ShortcutsModal({ projectId, open, onOpenChange, onSaved, onRun }: {
  projectId: string; open: boolean; onOpenChange: (o: boolean) => void; onSaved?: () => void; onRun?: (s: Shortcut) => void
}) {
  const { shortcuts, reload, loaded } = useShortcuts(open ? projectId : null)
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState<string | null>(null)
  const [query, setQuery] = useState('')
  // Single form state: null = closed, id null = adding, else editing.
  const [form, setForm] = useState<{ id: string | null; name: string; text: string } | null>(null)

  useEffect(() => {
    if (!open) return
    setError(null); setQuery(''); setForm(null)
  }, [open])

  const rows = useMemo(() => {
    const q = query.trim().toLowerCase()
    if (!q) return shortcuts
    return shortcuts.filter((s) => s.alias.toLowerCase().includes(q) || shortcutText(s).toLowerCase().includes(q))
  }, [shortcuts, query])

  async function persist(next: Shortcut[]) {
    setBusy(true); setError(null)
    try {
      // Shortcuts are the one server-backed list.
      await api(projectPath(projectId), { method: 'PATCH', body: JSON.stringify({ shortcuts: next }) })
      await reload(); onSaved?.()
    } catch (e) { setError(errMsg(e)) } finally { setBusy(false) }
  }

  function submitForm() {
    if (!form) return
    try {
      const clean = validateDraft(form, shortcuts, form.id ?? undefined)
      const row = toServerRow({ id: form.id ?? undefined, ...clean })
      setForm(null)
      void persist(form.id ? shortcuts.map((s) => (s.id === form.id ? row : s)) : [...shortcuts, row])
    } catch (e) { setError(errMsg(e)) }
  }

  const formIndex = form && form.id ? rows.findIndex((r) => r.id === form.id) : rows.length

  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent className="max-h-[85dvh] overflow-y-auto sm:max-w-lg">
        <DialogHeader>
          <DialogTitle>Shortcuts</DialogTitle>
          <DialogDescription>Run anything in one tap. Changes save instantly — type a command, or press keys like Ctrl-C.</DialogDescription>
        </DialogHeader>

        <div className="relative">
          <Search className="absolute top-1/2 left-3 size-4 -translate-y-1/2 text-muted-foreground" />
          <Input value={query} onChange={(e) => setQuery(e.target.value)} placeholder="Search shortcuts…" className="min-h-[44px] pl-9" aria-label="Search shortcuts" />
        </div>

        {!loaded ? (
          <div className="flex flex-col gap-2">
            <Skeleton className="h-14 w-full" /><Skeleton className="h-14 w-full" /><Skeleton className="h-14 w-full" />
          </div>
        ) : (
          <div className="flex flex-col gap-2">
            {form && form.id === null && (
              <ShortcutForm name={form.name} text={form.text} index={formIndex} busy={busy}
                onName={(name) => setForm({ ...form, name })} onText={(text) => setForm({ ...form, text })}
                onSubmit={submitForm} onCancel={() => { setForm(null); setError(null) }} submitLabel="Add" />
            )}
            {rows.length === 0 && !form && (
              <div className="flex flex-col items-center gap-2 rounded-lg border border-dashed px-4 py-8 text-center">
                <p className="text-sm font-medium">{query ? 'No matches' : 'No shortcuts yet'}</p>
                <p className="text-xs text-muted-foreground">{query ? 'Try a different search.' : 'Save the commands and keys you use most.'}</p>
                {!query && (
                  <Button size="sm" className="mt-1 min-h-[44px]" onClick={() => setForm({ id: null, name: '', text: '' })} data-testid="sc-add">
                    <Plus className="size-4" /> New shortcut
                  </Button>
                )}
              </div>
            )}
            {/* Rows are runnable only where onRun is wired (the terminal
                modal); the home modal renders the same rows inert so nobody
                expects a shortcut to execute from the home page. */}
            {rows.map((s, i) => form && form.id === s.id ? (
              <ShortcutForm key={s.id} name={form.name} text={form.text} index={i} busy={busy}
                onName={(name) => setForm({ ...form, name })} onText={(text) => setForm({ ...form, text })}
                onSubmit={submitForm} onCancel={() => { setForm(null); setError(null) }} submitLabel="Save" />
            ) : (
              <div key={s.id} role={onRun ? 'button' : undefined} tabIndex={onRun ? 0 : undefined} aria-label={onRun ? `Run ${s.alias}` : undefined}
                onClick={onRun ? () => onRun(s) : undefined}
                onKeyDown={onRun ? (e) => { if (e.key === 'Enter' || e.key === ' ') { e.preventDefault(); onRun(s) } } : undefined}
                data-testid={onRun ? `sc-row-${s.alias}` : undefined}
                className={`flex items-center gap-1 rounded-lg border px-2 py-1.5 ${onRun ? 'cursor-pointer hover:bg-accent/50' : ''}`}>
                <div className="min-w-0 flex-1 px-1">
                  <p className="text-sm font-medium wrap-break-word">{s.alias}</p>
                  <p className="font-mono text-xs wrap-break-word text-muted-foreground">{shortcutText(s)}</p>
                </div>
                <DropdownMenu>
                  <DropdownMenuTrigger asChild>
                    <Button variant="ghost" size="icon" aria-label={`Manage ${s.alias}`} data-testid={`sc-menu-${s.alias}`} className="size-11 shrink-0"
                      onClick={(e) => e.stopPropagation()} onKeyDown={(e) => e.stopPropagation()}>
                      <EllipsisVertical className="size-4" />
                    </Button>
                  </DropdownMenuTrigger>
                  <DropdownMenuContent align="end">
                    <DropdownMenuItem
                      onSelect={() => { setError(null); setForm({ id: s.id, name: s.alias, text: shortcutText(s) }) }}>
                      <Pencil className="size-4" /> Edit
                    </DropdownMenuItem>
                    <DropdownMenuItem variant="destructive" disabled={busy}
                      onSelect={() => void persist(shortcuts.filter((r) => r.id !== s.id))}>
                      <Trash2 className="size-4" /> Delete
                    </DropdownMenuItem>
                  </DropdownMenuContent>
                </DropdownMenu>
              </div>
            ))}
            {!form && rows.length > 0 && (
              <Button variant="outline" size="sm" onClick={() => setForm({ id: null, name: '', text: '' })} data-testid="sc-add" className="min-h-[44px]">
                <Plus className="size-4" /> New shortcut
              </Button>
            )}
          </div>
        )}

        {error && <p className="text-xs text-destructive" data-testid="sc-error">{error}</p>}
      </DialogContent>
    </Dialog>
  )
}

function ShortcutForm({ name, text, index, busy, onName, onText, onSubmit, onCancel, submitLabel }: {
  name: string; text: string; index: number; busy: boolean
  onName: (v: string) => void; onText: (v: string) => void
  onSubmit: () => void; onCancel: () => void; submitLabel: string
}) {
  return (
    <div className="flex flex-col gap-2 rounded-lg border border-primary/40 bg-muted/40 p-2">
      <Input placeholder="Name (e.g. dev)" aria-label="Shortcut name" value={name} onChange={(e) => onName(e.target.value)}
        className="min-h-[44px]" data-testid={`sc-alias-${index}`} />
      <Input placeholder="Command or keys — press Ctrl-C, Esc…" aria-label="Shortcut value" value={text}
        onChange={(e) => onText(e.target.value)}
        onKeyDown={(e) => {
          if (e.key === 'Enter' && !e.ctrlKey && !e.metaKey && !e.altKey) { e.preventDefault(); onSubmit(); return }
          const label = keydownToLabel(e); if (label) onText(label)
        }}
        className="min-h-[44px] font-mono" data-testid={`sc-command-${index}`} />
      <div className="flex justify-end gap-2">
        <Button variant="ghost" size="sm" onClick={onCancel} disabled={busy} className="min-h-[44px]">Cancel</Button>
        <Button size="sm" onClick={onSubmit} disabled={busy} data-testid="sc-save" className="min-h-[44px]">{submitLabel}</Button>
      </div>
    </div>
  )
}
