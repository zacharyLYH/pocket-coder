import { useMemo } from 'react'

import { highlight } from '@/lib/highlight'

// CodeBlock renders pre-highlighted HTML: generic token coloring, theme
// aware via the .hljs palette in index.css.
export function CodeBlock({ code, className }: { code: string; className?: string }) {
  const html = useMemo(() => highlight(code), [code])
  return (
    <pre className={className}>
      <code className="hljs" dangerouslySetInnerHTML={{ __html: html }} />
    </pre>
  )
}
