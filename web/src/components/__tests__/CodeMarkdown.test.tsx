import { render, screen } from '@testing-library/react'
import { describe, expect, it } from 'vitest'

import { CodeBlock } from '@/components/CodeBlock'
import { Markdown } from '@/components/Markdown'

describe('CodeBlock', () => {
  it('tags tokens without changing text', () => {
    const { container } = render(<CodeBlock code="func Verify(pin string) bool {" />)
    const code = container.querySelector('code')!
    expect(code.innerHTML).toContain('hljs-keyword')
    expect(code.textContent).toBe('func Verify(pin string) bool {')
  })

  it('renders empty code as empty', () => {
    const { container } = render(<CodeBlock code="" />)
    expect(container.querySelector('code')!.textContent).toBe('')
  })
})

describe('Markdown', () => {
  it('renders inline code and bold, keeps links inert', () => {
    render(<Markdown text="Calls `handleLogin()` and **writes** the row [docs](http://x)." />)
    expect(screen.getByText('handleLogin()').tagName).toBe('CODE')
    expect(screen.getByText('writes').tagName).toBe('STRONG')
    expect(screen.queryByRole('link')).toBeNull()
    expect(screen.getByText('docs')).toBeInTheDocument()
  })

  it('escapes raw html', () => {
    const { container } = render(<Markdown text='x <img src=x onerror=alert(1)> y' />)
    expect(container.querySelector('img')).toBeNull()
    expect(container.textContent).toContain('y')
  })
})
