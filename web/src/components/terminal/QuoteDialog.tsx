import { useEffect, useState } from 'react'

import { Button } from '@/components/ui/button'
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from '@/components/ui/dialog'
import { Textarea } from '@/components/ui/textarea'

// QuoteDialog opens from a hunk's Quote action: the hunk stays visible for
// context, and the comment box is right here instead of making the user
// scroll down to the notepad. Submitting appends one note block; it never
// rewrites the existing review.
export function QuoteDialog({
  open,
  path,
  hunk,
  onOpenChange,
  onAdd,
}: {
  open: boolean
  path: string
  hunk: string
  onOpenChange: (open: boolean) => void
  onAdd: (comment: string) => void
}) {
  const [comment, setComment] = useState('')

  useEffect(() => {
    if (open) setComment('')
  }, [open])

  function add() {
    onAdd(comment.trim())
    onOpenChange(false)
  }

  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent className="max-h-[calc(100dvh-2rem)] grid-rows-[auto_minmax(0,1fr)_auto] overflow-hidden p-4 sm:max-w-xl" data-testid="quote-dialog">
        <DialogHeader>
          <DialogTitle>Quote hunk</DialogTitle>
          <DialogDescription className="truncate font-mono">{path}</DialogDescription>
        </DialogHeader>
        <div className="flex min-h-0 flex-col gap-3 overflow-y-auto">
          <pre className="max-h-44 overflow-auto rounded-md border bg-muted/40 p-2 font-mono text-[11px] whitespace-pre-wrap">
            {hunk.trim()}
          </pre>
          <Textarea
            value={comment}
            onChange={(e) => setComment(e.target.value)}
            placeholder="What should the AI change, fix, or keep?"
            className="min-h-24 resize-y font-mono text-xs"
            autoFocus
            data-testid="quote-comment"
          />
        </div>
        <DialogFooter className="gap-2">
          <Button type="button" variant="outline" onClick={() => onOpenChange(false)} data-testid="quote-cancel">
            Cancel
          </Button>
          <Button type="button" onClick={add} data-testid="quote-add">
            Add to notes
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  )
}
