import { render, screen } from '@testing-library/react'
import { describe, expect, it, vi } from 'vitest'

import { PreviewSurface } from '@/components/PreviewSurface'

describe('PreviewSurface', () => {
  it('loads the authenticated SPS surface, not the project URL', () => {
    render(<PreviewSurface projectId="project/one" onBack={vi.fn()} />)
    const frame = screen.getByTitle('Remote project preview')
    expect(frame).toHaveAttribute('src', '/api/projects/project%2Fone/preview/vnc_lite.html?autoconnect=true&resize=scale&reconnect=1&reconnect_delay=2000&path=api%2Fprojects%2Fproject%252Fone%2Fpreview%2Fwebsockify')
  })
})
