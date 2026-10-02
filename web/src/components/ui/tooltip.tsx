import * as TooltipPrimitive from '@radix-ui/react-tooltip'

import { cn } from '@/lib/utils'

export const TooltipProvider = TooltipPrimitive.Provider
export const Tooltip = TooltipPrimitive.Root
export const TooltipTrigger = TooltipPrimitive.Trigger
export const TooltipContent = ({
  className,
  sideOffset,
  ...props
}: React.ComponentPropsWithoutRef<typeof TooltipPrimitive.Content>) => (
  <TooltipPrimitive.Portal>
    <TooltipPrimitive.Content
      sideOffset={sideOffset}
      className={cn(
        'z-50 overflow-hidden rounded-md bg-popover px-[0.45rem] py-1.5 text-xs text-popover-foreground shadow-md',
        'data-[state=delayed-open]:data-[side=top]:slide-in-from-bottom-1',
        'data-[state=delayed-open]:data-[side=bottom]:slide-in-from-top-1',
        'data-[state=delayed-open]:data-[side=left]:slide-in-from-right-1',
        'data-[state=delayed-open]:data-[side=right]:slide-in-from-left-1',
        className,
      )}
      {...props}
    />
  </TooltipPrimitive.Portal>
)
TooltipContent.displayName = TooltipPrimitive.Content.displayName
