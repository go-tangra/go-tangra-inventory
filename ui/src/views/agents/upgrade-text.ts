// Human text for fleet states and reason codes. The server sends codes only;
// unknown codes are shown as-is rather than hidden.
import type { FleetState, SkipReason } from '@/api/types'

// Fleet states from which a routine "Upgrade" is offered.
export const UPGRADABLE: ReadonlySet<FleetState> = new Set<FleetState>(['available', 'failed', 'rolled_back'])

const STATE_LABELS: Record<FleetState, string> = {
  up_to_date: 'Up to date',
  available: 'Update available',
  pending: 'Pending',
  in_progress: 'Upgrading',
  failed: 'Failed',
  rolled_back: 'Rolled back',
  manual_upgrade_required: 'Manual upgrade required',
  unsupported: 'Unsupported',
}

export function fleetStateLabel(s: FleetState): string {
  return STATE_LABELS[s] ?? s
}

const REASONS: Record<string, string> = {
  signature_invalid: 'The release signature did not verify',
  unknown_key: 'The release was signed with a key the agent does not trust',
  checksum_mismatch: 'The download did not match the signed checksum',
  size_mismatch: 'The download size did not match the signed release',
  platform_mismatch: 'No artifact for this platform in the release',
  version_mismatch: 'The release version did not match the request',
  downgrade_refused: 'Downgrade refused by the agent',
  disk_full: 'Not enough free disk space on the host',
  download_failed: 'The download failed',
  install_failed: 'The package installation failed',
  start_timeout: 'The new version did not confirm in time and was rolled back',
  unsupported_install: 'Self-upgrade is not supported on this host (systemd required); upgrade manually',
  busy: 'Another upgrade was already running on the host',
  package_db_mismatch: 'Rolled back by restoring the binary; the package database may still list the new version',
  cancelled_locally: 'Cancelled on the host',
  expired: 'The agent did not pick up the request in time',
  up_to_date: 'Already on the target version',
  upgrade_active: 'An upgrade is already in progress',
  manual_upgrade_required: 'Agent older than 4.4.0: install the current version by hand once',
  unsupported: 'The agent does not support self-upgrade',
  no_release_for_platform: 'No release for this platform',
  not_found: 'Agent not found',
  not_comparable: 'The installed version cannot be compared (development build)',
}

export function reasonText(code: string): string {
  return REASONS[code] ?? code
}

// skipSummary renders the skipped agents of a batch as " N skipped: reason (count), …".
export function skipSummary(skipped: { agent_id: string; reason: SkipReason }[]): string {
  if (!skipped.length) return ''
  const counts = new Map<string, number>()
  for (const s of skipped) counts.set(s.reason, (counts.get(s.reason) ?? 0) + 1)
  const parts = [...counts].map(([r, n]) => `${reasonText(r).toLowerCase()} (${n})`)
  return ` ${skipped.length} skipped: ${parts.join(', ')}.`
}
