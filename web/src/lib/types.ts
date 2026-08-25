// Shared API shapes. One declaration per backend payload, imported
// everywhere instead of re-declared per component.
export type Project = { id: string; name: string }
export type SSHKey = { fingerprint: string; publicKey: string; label: string }
export type Harness = { id: string; name: string; command: string; install?: string; installed?: boolean }
export type ExecResult = { project: string; status: 'ok' | 'skipped' | 'error'; detail?: string }
