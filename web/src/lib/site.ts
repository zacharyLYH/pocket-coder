// Public-site constants for the landing page. No host gating: / is the
// landing page on every host, /login is login, /app is the app.
export const LINKEDIN_URL = 'https://my.linkedin.com/in/leeyihong03'
export const BIO_URL = 'https://bio.bytesbylyh.dev'
export const GITHUB_URL = 'https://github.com/zacharyLYH/pocket-coder'
export const GITHUB_KEYS_URL = 'https://github.com/settings/keys'
export const GMAIL_GUIDE_URL =
  'https://help.meetalfred.com/en/articles/8160682-set-up-smtp-for-gmail-app-password-guide'
export const SETUP_SCRIPT_URL =
  'https://raw.githubusercontent.com/zacharyLYH/pocket-coder/main/deploy/setup.sh'

// shellQuote keeps the generated installer command paste-safe.
export function shellQuote(s: string): string {
  if (/^[A-Za-z0-9_@%+=:,./-]+$/.test(s) && s.length > 0) return s
  return `'${s.replace(/'/g, `'\\''`)}'`
}

export function buildSetupCommand(email: string, smtpPassword: string): string {
  const e = email.trim() || 'you@example.com'
  const p = smtpPassword || 'xxxx app password'
  return `curl -fsSL ${SETUP_SCRIPT_URL} | bash -s -- --email ${shellQuote(e)} --smtp-password ${shellQuote(p)}`
}
