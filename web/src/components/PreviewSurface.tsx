import { previewSurfacePath } from '@/lib/preview'

// The project is not loaded in this iframe. It loads the authenticated noVNC
// surface, which keeps the browser chrome and project traffic server-side.
export function PreviewSurface({ projectId, onBack }: { projectId: string; onBack: () => void }) {
  return (
    <main className="flex min-h-dvh flex-col">
      <header className="flex items-center gap-3 border-b p-3">
        <button type="button" className="text-sm text-muted-foreground" onClick={onBack}>Back</button>
        <h1 className="text-sm font-semibold">Preview</h1>
      </header>
      <iframe
        title="Remote project preview"
        className="min-h-0 flex-1 border-0"
        src={previewSurfacePath(projectId)}
        allow="clipboard-read; clipboard-write"
      />
    </main>
  )
}
