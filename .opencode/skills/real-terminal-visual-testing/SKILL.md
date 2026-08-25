---
name: real-terminal-visual-testing
description: Use when validating browser-rendered terminal UIs whose appearance depends on a real CLI, tmux, ANSI/truecolor escape sequences, Unicode glyphs, or backend PTY negotiation.
---

# Real terminal visual testing

Mocked WebSocket output is useful for terminal behavior and layout, but it
cannot prove that a real TUI renders correctly. Full-screen CLIs may depend on
their own startup timing, Unicode block glyphs, ANSI SGR sequences, truecolor,
tmux settings, and terminal dimensions.

For a rendering bug:

1. Add a Playwright test that boots the repository’s real backend and Docker
   sandbox. Follow the current `playwright.config` first: it may use an auth
   setup project and storage state, so do not assume a special environment flag
   or repeat the PIN login inside every test.
2. Create the project and launch the real harness through the UI. Select the
   exact session from the UI dropdown; a URL alone does not prove the intended
   session is attached.
3. Wait for visible application content, not merely `.xterm-screen` or a
   `Connected` badge. A terminal canvas can be mounted and connected while the
   TUI is still blank.
4. Save a screenshot artifact and assert it with `toHaveScreenshot`. Keep the
   baseline under the spec’s `*-snapshots/` directory. Regenerate it only after
   manually inspecting the rendered result.
5. If colors look wrong, capture the raw tmux/PTY output with escape sequences.
   Distinguish a browser renderer problem from colors deliberately emitted by
   the CLI. Truecolor output such as `ESC[38;2;R;G;Bm` and
   `ESC[48;2;R;G;Bm` should not be “fixed” by changing xterm’s 16-color theme.
6. Clean up projects and temporary containers in `finally`; never put session
   tokens or credentials in test files, screenshots, or logs.

The real-stack test is the source of truth for TUI fidelity. Keep mocked tests
for fast interaction coverage, but do not treat their screenshots as evidence
that a real CLI’s visual output is correct.
