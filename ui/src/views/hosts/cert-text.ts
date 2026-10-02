// Human text for certificate delivery states, reason codes and the agent
// certificate capability (feature 033). The server sends codes only;
// unknown codes are shown as-is rather than hidden.
import type { CertificateCapability, DeliveryState } from '@/api/types'

type Color = 'neutral' | 'primary' | 'info' | 'success' | 'warning' | 'error'

const STATES: Record<DeliveryState, { label: string; color: Color }> = {
  pending: { label: 'queued', color: 'neutral' },
  delivered: { label: 'sent to agent', color: 'info' },
  fetched: { label: 'installing', color: 'primary' },
  installed: { label: 'installed', color: 'success' },
  unchanged: { label: 'unchanged', color: 'success' },
  failed: { label: 'failed', color: 'error' },
  hook_failed: { label: 'hook failed', color: 'warning' },
  unsupported: { label: 'unsupported', color: 'neutral' },
  superseded: { label: 'superseded', color: 'neutral' },
  expired: { label: 'expired', color: 'warning' },
  cancelled: { label: 'cancelled', color: 'neutral' },
}

/** Chip colours by state (UiStatusChip). */
export const STATE_COLORS = Object.fromEntries(Object.entries(STATES).map(([k, v]) => [k, v.color])) as Record<DeliveryState, Color>

export function stateLabel(s: string): string {
  return STATES[s as DeliveryState]?.label ?? s
}

const REASONS: Record<string, string> = {
  // reported by the agent
  invalid_name: 'The certificate name is not valid on the host',
  invalid_bundle: 'The certificate bundle is not valid',
  key_mismatch: 'The private key does not match the certificate',
  certificate_not_valid: 'The certificate is not valid yet or no longer',
  owner_unknown: 'The configured file owner or group does not exist on the host',
  write_failed: 'The files could not be written on the host',
  disk_full: 'Not enough free disk space on the host',
  hook_failed: 'The deploy hook failed',
  hook_timeout: 'The deploy hook timed out',
  hook_refused: 'The deploy hook was refused (not a root-owned executable)',
  disabled_locally: 'Certificates are disabled in the agent configuration',
  busy: 'The agent was busy with another certificate',
  // set by the server
  key_unavailable: 'The private key is not available in lcm',
  certificate_revoked: 'The certificate was revoked',
  certificate_expired: 'The certificate expired',
  certificate_not_found: 'The certificate no longer exists in lcm',
  lcm_unavailable: 'lcm was not reachable',
  bundle_too_large: 'The certificate bundle is too large',
  fingerprint_mismatch: 'The agent installed a different certificate than the one served',
  no_agent: 'The host has no agent',
  no_capability: 'The agent cannot receive certificates',
  platform: 'Certificate delivery is not supported on this platform',
  host_retired: 'The host is retired',
  agent_revoked: 'The agent was revoked',
  host_deleted: 'The host was deleted',
  no_report: 'The agent did not report a result in time',
  expired: 'The agent did not pick up the certificate in time',
  older_than_installed: 'An equal or newer certificate is already installed',
  cancelled_by_user: 'Cancelled by a user',
  unknown_host: 'The host is unknown',
}

export function reasonText(code: string | undefined | null): string {
  if (!code) return ''
  return REASONS[code] ?? code
}

/** Deploy hook result: -1 not run, 256 timed out, otherwise the exit code. */
export function hookText(code: number | null | undefined): string {
  if (code === null || code === undefined) return ''
  if (code === -1) return 'not run'
  if (code === 256) return 'timed out'
  return code === 0 ? 'ok (0)' : 'exit ' + String(code)
}

/** A fingerprint shortened for tables (the copy button carries it whole). */
export function shortFingerprint(fp: string | undefined | null): string {
  if (!fp) return ''
  return fp.length > 20 ? fp.slice(0, 12) + '…' + fp.slice(-6) : fp
}

const CAPABILITIES: Record<CertificateCapability, { label: string; color: Color; text: string }> = {
  enabled: { label: 'enabled', color: 'success', text: 'The agent announces certificate support' },
  disabled_on_host: { label: 'disabled on host', color: 'warning', text: 'Certificates are disabled in the agent configuration (certificates.enabled)' },
  upgrade_required: { label: 'upgrade required', color: 'warning', text: 'The agent is older than 4.7.0; upgrade it to receive certificates' },
  not_supported_platform: { label: 'not supported', color: 'neutral', text: 'Certificate delivery is not supported on this platform (Windows)' },
  disabled_on_server: { label: 'off', color: 'neutral', text: 'Certificate delivery is switched off on the server' },
}

export const CAPABILITY_COLORS = Object.fromEntries(Object.entries(CAPABILITIES).map(([k, v]) => [k, v.color])) as Record<CertificateCapability, Color>

export function capabilityLabel(c: string): string {
  return CAPABILITIES[c as CertificateCapability]?.label ?? c
}

export function capabilityText(c: string): string {
  return CAPABILITIES[c as CertificateCapability]?.text ?? c
}

/** Deployer link of a configuration (the federated deployer module). */
export function deployerLink(configurationId: string): string {
  return '/deployer/configurations?id=' + encodeURIComponent(configurationId)
}

const TRIGGERS: Record<string, string> = { manual: 'manual', auto_deploy: 'auto deploy', retry: 'retry' }

export function triggerLabel(t: string): string {
  return TRIGGERS[t] ?? t
}

/** Live delivery events are bursty (one per item transition): reload once they settle. */
export const RELOAD_DEBOUNCE_MS = 300
