// A vibrant dark theme (Tokyonight-ish): the server's tmux status styling
// (ThemeArgs in server/internal/session/session.go) is tuned to sit next to
// it. Muted greys on near-black read as dead text; saturated ANSI colours
// make successful lines (green), failures (red) and paths (blue) pop.
export const TERM_THEME = {
  background: '#1a1b26',
  foreground: '#c0caf5',
  cursor: '#c0caf5',
  cursorAccent: '#1a1b26',
  selectionBackground: '#283247',
  selectionForeground: '#c0caf5',
  black: '#15161e',
  red: '#f7768e',
  green: '#9ece6a',
  yellow: '#e0af68',
  blue: '#7aa2f7',
  magenta: '#bb9af7',
  cyan: '#7dcfff',
  white: '#a9b1d6',
  brightBlack: '#414868',
  brightRed: '#f7768e',
  brightGreen: '#9ece6a',
  brightYellow: '#e0af68',
  brightBlue: '#7aa2f7',
  brightMagenta: '#bb9af7',
  brightCyan: '#7dcfff',
  brightWhite: '#c0caf5',
  // The 7px scrollbar (see `overviewRuler` below) needs its own colours or
  // it falls back to a chrome-grey bar that fights the theme.
  scrollbarSliderBackground: '#56576a',
  scrollbarSliderHoverBackground: '#7aa2f7',
  scrollbarSliderActiveBackground: '#7aa2f7',
}
