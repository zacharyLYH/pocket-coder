---
name: xterm6-pane-tuning
description: Use when changing xterm.js look/feel in the browser pane (theme, fonts, scrollbar, fit/resize behaviour) — probe xterm 6 in headless Chrome first so CSS selectors and options are grounded in measured DOM facts instead of guesses.
---

# Tuning the xterm 6 pane

Every fact below was measured (not read from docs) with
`scripts/probe.mjs`: it boots xterm's UMD bundle in headless Chrome, writes
scripted ANSI output, and dumps the DOM tree + computed styles. Run it before
writing CSS or picking options — xterm's internal DOM changes between majors
and its docs lag the code.

```
node skills/xterm6-pane-tuning/scripts/probe.mjs            # dump DOM + metrics
node skills/xterm6-pane-tuning/scripts/probe.mjs --shot /tmp/pane.png
node skills/xterm6-pane-tuning/scripts/probe.mjs --webgl    # after `npm i --no-save @xterm/addon-webgl` in web/
```

The `--webgl` run also reports `renderer`, `webglError` and `domRowsExist`, which
is the quickest way to see the renderer's side effects (see the WebGL section
below).

The script needs `web/node_modules/@playwright/test` and the repo's Chrome
(`channel: 'chrome'`); it starts no app, no server, no Docker, so the loop is
seconds instead of minutes.

## Sharp edges (xterm 6.0.0)

1. **Injected UMD bundle names are counter-intuitive.** `xterm.js` copies its
   exports onto `window` (`window.Terminal` is the class), but
   `addon-fit.js` assigns the whole module namespace: use
   `new window.FitAddon.FitAddon()`.
2. **No `.xterm-scrollbar` element exists.** The native-looking scrollbar is
   `.xterm-scrollable-element > .scrollbar.vertical > .slider` (plus
   `.scrollbar.horizontal`). Any CSS copied from older xterm versions (or from
   a spec that mentions `.xterm-scrollbar`) silently targets nothing.
3. **One option controls both the scrollbar width and the fit's column
   budget.** `options.overviewRuler.width` feeds
   `verticalScrollbarSize: overviewRuler?.width || 14`, and `FitAddon`
   subtracts the same value from the available width
   (`t = scrollback === 0 ? 0 : options.overviewRuler?.width || 14`). So
   `overviewRuler: { width: 7 }` halves the visible scrollbar **and** reclaims
   the reserved gutter as real columns; `0` is falsy and falls back to 14. A
   custom `proposeDimensions()` override is unnecessary — and poking
   `term._core._renderService.dimensions` is private API that breaks on
   upgrade.
4. Setting `overviewRuler` also mounts a decoration canvas
   (`.xterm-decoration-overview-ruler`, `pointer-events: none`, `z-index: 8`)
   over the scrollbar gutter. It renders nothing without decorations and does
   not steal pointer events from the scrollbar, but it is there — assert layout
   numbers, not canvas absence.
5. **Theme background lands on `.xterm-scrollable-element`** (inline style).
   `.xterm-viewport` keeps xterm.css's `background-color: #000`, and
   `.xterm-screen` / `.xterm-rows` stay transparent. Reading the background off
   the viewport returns `rgb(0, 0, 0)` even when the theme is applied.
6. **`fontWeight` / `fontWeightBold` are applied to row `<span>`s** through a
   generated stylesheet (`.xterm-bold` gets the bold weight), not to the row
   `div`s (they stay `400`). The four `.xterm-char-measure-element` spans get
   the weight inline and drive cell width. To assert typography, read a
   rendered span, not the row.
7. **Cursor blink is a CSS animation** injected per terminal instance, so
   `toHaveScreenshot({ animations: 'disabled' })` (the repo default) pins it.
   No `caret: 'hide'` needed for the xterm cursor.
8. The terminal only fills the pane after `fit.fit()`; `term.open()` alone
   leaves it at the default 80x24 with `.xterm-screen` sized to its content —
   which is why "the terminal does not fill the width" bugs are usually a
   missing/early fit, not a renderer problem.
9. `WebglAddon.activate(term)` is **synchronous** (returns nothing), so
   `addon.activate(term).catch(...)` is a TypeError and `await` is a no-op. Wrap
   it in try/catch, and never call it before the first paint if you need
   `.xterm-rows` (it is deleted the moment it activates).

## WebGL addon: measure before adopting

`@xterm/addon-webgl@0.19.0` does install and `activate()` against xterm 6.0.0
(no peer deps declared, no throw in headless Chrome), so it is *not* blocked on
compatibility. It is blocked on value. A/B the identical burst before believing
the "WebGL is smoother" folk wisdom:

| burst of 40 coalesced frames × 40 lines | burst | frame p95 | long tasks |
|---|---|---|---|
| headless DOM (SwiftShader) | 644ms | 17ms | 0 |
| headless WebGL | 645ms | 18ms | 1 (436ms) |
| headed DOM (real GPU) | 316ms | 9ms | 0 |
| headed WebGL | 359ms | 9ms | 0 |

xterm 6 renders DOM rows differentially (only changed rows repaint), and once
writes are coalesced per frame there is no thrash left for the GPU to remove.
Also, activating WebGL **deletes `.xterm-rows`**: `document.querySelectorAll('.xterm-rows')`
returns nothing, so every DOM-text assertion in a suite (and browser
find-in-page / the a11y tree in the real app) silently stops working. Re-check
those two costs before adopting; on xterm 4/5 the answer was different.

Reproduce with `probe.mjs --webgl` (needs `npm i --no-save @xterm/addon-webgl`
in `web/`): it prints `renderer: webgl`, `domRowsExist: false` and `dotCols: 0`
(the DOM-text column measurement above simply stops working), against
`renderer: dom`, `domRowsExist: true`, `dotCols: 174` without the addon. For
frame-level numbers, run each renderer in its own throwaway burst script (N
frames × K lines inside one `requestAnimationFrame` loop) and report
`PerformanceObserver` long tasks plus rAF deltas — do not add the addon to the
app just to see the numbers.

## Asserting "spaciousness" from the DOM

`.xterm-rows > div` has no fixed width, so read the column count off the
content: write a line of dots longer than any viewport, then the length of the
first all-dots row **is** `term.cols`. With that plus the measured cell width
(`screenWidth / cols`) you can assert, platform-independently, that
`cols === floor((paneWidth - scrollbarWidth) / cellWidth)` after a resize.