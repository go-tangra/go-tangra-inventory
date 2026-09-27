import { z } from 'zod'

const hhmm = /^([01][0-9]|2[0-3]):[0-5][0-9]$/
const release = /^(0|[1-9][0-9]{0,8})\.(0|[1-9][0-9]{0,8})\.(0|[1-9][0-9]{0,8})(-[0-9A-Za-z-]+(\.[0-9A-Za-z-]+)*)?$/

// validTimezone accepts IANA names the browser knows; the server checks again
// with time.LoadLocation.
export function validTimezone(tz: string): boolean {
  if (!tz || tz === 'Local' || tz.length > 64) return false
  try {
    new Intl.DateTimeFormat('en', { timeZone: tz })
    return true
  } catch {
    return false
  }
}

/** PUT /agents/upgrade-policy payload. */
export const upgradePolicySchema = z.object({
  enabled: z.boolean(),
  window_start: z.string().trim().regex(hhmm, 'use HH:MM (00:00-23:59)'),
  window_end: z.string().trim().regex(hhmm, 'use HH:MM (00:00-23:59)'),
  timezone: z.string().trim().refine(validTimezone, 'unknown timezone (use an IANA name such as Europe/Sofia)'),
  max_concurrent: z.coerce.number().int().min(1, 'at least 1').max(100, 'at most 100'),
  target_version: z
    .string()
    .trim()
    .max(64)
    .refine((v) => v === '' || release.test(v), 'a release version such as 4.5.0, or empty for the current version'),
})
export type UpgradePolicyInput = z.input<typeof upgradePolicySchema>
