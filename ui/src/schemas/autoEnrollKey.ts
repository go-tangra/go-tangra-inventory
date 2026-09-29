import { z } from 'zod'

const v4 = /^(25[0-5]|2[0-4]\d|1\d\d|[1-9]?\d)(\.(25[0-5]|2[0-4]\d|1\d\d|[1-9]?\d)){3}$/

/** isNetwork accepts an IPv4/IPv6 address or network ("/0" is refused). The server re-validates. */
export function isNetwork(raw: string): boolean {
  const [addr = '', bits, ...rest] = raw.trim().split('/')
  if (rest.length) return false
  const v6 = addr.includes(':')
  if (v6 ? !/^[0-9A-Fa-f:.]+$/.test(addr) || addr.split('::').length > 2 : !v4.test(addr)) return false
  if (bits === undefined) return true
  if (!/^\d{1,3}$/.test(bits)) return false
  const n = Number(bits)
  return n >= 1 && n <= (v6 ? 128 : 32)
}

/** parseNetworks splits a comma/whitespace/newline separated list. */
export function parseNetworks(text: string): string[] {
  return text
    .split(/[\s,]+/)
    .map((s) => s.trim())
    .filter(Boolean)
}

/** The create/edit form of an auto-enrollment key. */
export const autoEnrollKeySchema = z.object({
  name: z.string().trim().min(1, 'required').max(100),
  networks: z
    .string()
    .refine((t) => parseNetworks(t).length > 0, 'at least one network, e.g. 10.0.0.0/8')
    .refine((t) => parseNetworks(t).length <= 32, 'at most 32 networks')
    .refine((t) => parseNetworks(t).every(isNetwork), 'use addresses or networks such as 10.0.0.0/8 or 2001:db8::/32 (not /0)'),
  expires_on: z
    .string()
    .refine((d) => d === '' || new Date(d + 'T23:59:59').getTime() > Date.now(), 'must be in the future'),
  max_enrollments: z.coerce.number().int().min(0, '0 = unlimited').max(1_000_000),
})
export type AutoEnrollKeyForm = z.output<typeof autoEnrollKeySchema>
