import { Moon, Sun } from 'lucide-react'

import { Button } from '@/components/ui/button'
import { useTheme } from '@/lib/useTheme'

// ThemeToggle flips the `dark` class shadcn's dark variant keys off
// (see index.css). Persists to localStorage; an inline script in index.html
// applies the saved class before paint to avoid a flash.
export function ThemeToggle() {
  const [dark, setDark] = useTheme()

  return (
    <Button
      variant="ghost"
      size="sm"
      onClick={() => setDark(!dark)}
      aria-label={dark ? 'Switch to light theme' : 'Switch to dark theme'}
    >
      {dark ? <Sun className="size-4" /> : <Moon className="size-4" />}
    </Button>
  )
}
