import { describe, expect, it } from 'vitest'
import { autoEnrollKeySchema, isNetwork, parseNetworks } from '@/schemas'

describe('auto-enrollment key form', () => {
  it('accepts addresses and networks, refuses /0 and garbage', () => {
    for (const ok of ['10.0.0.0/8', '192.0.2.10', '2001:db8::/32', '::1', '10.1.2.3/32', 'fe80::1/64']) expect(isNetwork(ok), ok).toBe(true)
    for (const bad of ['0.0.0.0/0', '::/0', '10.0.0.0/33', '256.1.1.1', 'nonsense', '10.0.0.0/8/1', '1::2::3', '2001:db8::/129', '10.0.0.0/x', '']) expect(isNetwork(bad), bad).toBe(false)
  })

  it('splits lists on newlines, commas and spaces', () => {
    expect(parseNetworks(' 10.0.0.0/8,\n192.0.2.1  2001:db8::/32\n\n')).toEqual(['10.0.0.0/8', '192.0.2.1', '2001:db8::/32'])
  })

  it('validates the whole form', () => {
    const base = { name: 'lab', networks: '10.0.0.0/8', expires_on: '', max_enrollments: 0 }
    expect(autoEnrollKeySchema.safeParse(base).success).toBe(true)
    expect(autoEnrollKeySchema.safeParse({ ...base, name: ' ' }).success).toBe(false)
    expect(autoEnrollKeySchema.safeParse({ ...base, networks: '' }).success).toBe(false)
    expect(autoEnrollKeySchema.safeParse({ ...base, networks: '0.0.0.0/0' }).success).toBe(false)
    expect(autoEnrollKeySchema.safeParse({ ...base, networks: Array.from({ length: 33 }, (_, i) => `10.${i}.0.0/16`).join('\n') }).success).toBe(false)
    expect(autoEnrollKeySchema.safeParse({ ...base, expires_on: '2000-01-01' }).success).toBe(false)
    expect(autoEnrollKeySchema.safeParse({ ...base, expires_on: '2999-01-01' }).success).toBe(true)
    expect(autoEnrollKeySchema.safeParse({ ...base, max_enrollments: -1 }).success).toBe(false)
  })
})
