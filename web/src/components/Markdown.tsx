import ReactMarkdown from 'react-markdown'

// Markdown renders model summaries: inline code, bold, and lists. Raw
// HTML is never rendered (react-markdown escapes it by default) and links
// stay inert text, so summaries cannot navigate or inject markup.
export function Markdown({ text, className }: { text: string; className?: string }) {
  return (
    <div className={className}>
      <ReactMarkdown
        components={{
          p: ({ children }) => <p className="m-0">{children}</p>,
          code: ({ children }) => <code className="rounded bg-muted px-1 py-0.5 font-mono text-[12px]">{children}</code>,
          // eslint-disable-next-line @typescript-eslint/no-unused-vars
          a: ({ children, ..._rest }) => <span>{children}</span>,
        }}
      >
        {text}
      </ReactMarkdown>
    </div>
  )
}
