// Public-site constants for the landing page. No host gating: / is the
// landing page on every host, /login is login, /app is the app.
export const LINKEDIN_URL = 'https://my.linkedin.com/in/leeyihong03'
export const BIO_URL = 'https://bio.bytesbylyh.dev'
export const GITHUB_URL = 'https://github.com/zacharyLYH/pocket-coder'
export const GITHUB_KEYS_URL = 'https://github.com/settings/keys'
const SETUP_SCRIPT_URL =
  'https://raw.githubusercontent.com/zacharyLYH/pocket-coder/main/deploy/setup.sh'

// shellQuote keeps the generated installer command paste-safe.
function shellQuote(s: string): string {
  if (/^[A-Za-z0-9_@%+=:,./-]+$/.test(s) && s.length > 0) return s
  return `'${s.replace(/'/g, `'\\''`)}'`
}

export function buildSetupCommand(email: string, password: string): string {
  const e = email.trim() || 'you@example.com'
  const p = password || 'your-login-password'
  return `curl -fsSL ${SETUP_SCRIPT_URL} | bash -s -- --email ${shellQuote(e)} --password ${shellQuote(p)}`
}
