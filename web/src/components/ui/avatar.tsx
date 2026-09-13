import type { HTMLAttributes } from 'react'

import { cn } from '@/lib/utils'

// Avatar in the shadcn shape (a circle with an icon or initials inside).
// Radix-free on purpose: the chat only needs a static glyph holder, not
// image loading states.
export function Avatar({ className, ...props }: HTMLAttributes<HTMLDivElement>) {
  return (
    <div
      className={cn('flex size-8 shrink-0 items-center justify-center rounded-full bg-muted', className)}
      {...props}
    />
  )
}
