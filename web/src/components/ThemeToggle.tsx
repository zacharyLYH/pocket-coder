import { useEffect, useState } from 'react'
import { Moon, Sun } from 'lucide-react'

import { Button } from '@/components/ui/button'

const STORAGE_KEY = 'pcoder-theme'

function initialDark(): boolean {
  if (typeof document !== 'undefined' && document.documentElement.classList.contains('dark')) return true
  try {
    const saved = localStorage.getItem(STORAGE_KEY)
    if (saved) return saved === 'dark'
  } catch {
    // storage unavailable — fall through to media query
  }
  return window.matchMedia?.('(prefers-color-scheme: dark)').matches ?? false
}

// useTheme owns the dark class + persistence so menus can flip the theme
// without mounting the toggle button.
export function useTheme(): [boolean, (dark: boolean) => void] {
  const [dark, setDark] = useState(initialDark)

  useEffect(() => {
    document.documentElement.classList.toggle('dark', dark)
    try {
      localStorage.setItem(STORAGE_KEY, dark ? 'dark' : 'light')
    } catch {
      // storage unavailable — theme still applies for this session
    }
  }, [dark])

  return [dark, setDark]
}

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
      data-testid="theme-toggle"
    >
      {dark ? <Sun className="size-4" /> : <Moon className="size-4" />}
    </Button>
  )
}
