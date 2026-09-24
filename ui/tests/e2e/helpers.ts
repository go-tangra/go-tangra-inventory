import { createHmac } from 'node:crypto'
import { existsSync, mkdirSync, readFileSync, writeFileSync } from 'node:fs'
import { join } from 'node:path'
import { tmpdir } from 'node:os'
import { expect, type Page } from '@playwright/test'

const base = process.env.E2E_BASE ?? 'https://localhost:8443'

/** RFC 6238 TOTP (SHA-1, 6 digits, 30 s) from a base32 secret. */
export function totp(secretBase32: string, at = Date.now()): string {
  const alphabet = 'ABCDEFGHIJKLMNOPQRSTUVWXYZ234567'
  let bits = ''
  for (const c of secretBase32.replace(/=+$/, '').toUpperCase()) bits += alphabet.indexOf(c).toString(2).padStart(5, '0')
  const bytes = bits.match(/.{8}/g) ?? []
  if (!bytes.length) throw new Error('totp: empty secret')
  const key = Buffer.from(bytes.map((b) => parseInt(b, 2)))
  const counter = Buffer.alloc(8)
  counter.writeBigUInt64BE(BigInt(Math.floor(at / 30000)))
  const h = createHmac('sha1', key).update(counter).digest()
  const off = h[h.length - 1]! & 0xf
  const code = ((h.readUInt32BE(off) & 0x7fffffff) % 1_000_000).toString().padStart(6, '0')
  return code
}

/**
 * Signs the platform operator in through the gateway. The platform tenant
 * requires a second factor: the first sign-in enrols TOTP through the auth
 * remote's enrolment page, later sign-ins answer the challenge.
 */
export async function signIn(page: Page, email: string, password: string): Promise<void> {
  await page.goto(base + '/console/signin')
  await page.getByLabel('Organisation').fill('platform')
  await page.getByLabel('Email').fill(email)
  await page.getByLabel('Password').fill(password)
  await page.getByRole('button', { name: /sign in/i }).click()
  const challenge = page.getByTestId('mfa')
  await expect(page.getByTestId('nav-home').or(page.getByTestId('mfa-enrol')).or(challenge).first()).toBeVisible({ timeout: 15_000 })
  if (await page.getByTestId('mfa-enrol').isVisible()) {
    await expect(page.getByTestId('secret')).not.toHaveText('')
    const secret = (await page.getByTestId('secret').textContent())?.trim() ?? ''
    rememberSecret(secret)
    rememberPeriod(Math.floor(Date.now() / 30000))
    await page.getByTestId('code').locator('input').fill(totp(secret))
    await page.getByTestId('confirm').click()
    await expect(page.getByTestId('recovery-codes')).toBeVisible()
    await page.getByTestId('saved').locator('input').check()
    await page.getByTestId('done').click()
  } else if (await challenge.isVisible()) {
    // A TOTP code is single-use per 30 s window: wait for a fresh window when needed.
    const period = Math.floor(Date.now() / 30000)
    if (period === lastPeriod()) await page.waitForTimeout((period + 1) * 30000 - Date.now() + 200)
    rememberPeriod(Math.floor(Date.now() / 30000))
    await page.getByTestId('code').locator('input').fill(totp(storedSecret()))
    await page.getByTestId('submit').click()
  }
  await expect(page.getByTestId('nav-home')).toBeVisible({ timeout: 15_000 })
}

// The enrolled TOTP secret is shared across spec files (workers) through a
// file so later sign-ins can answer the challenge.
const secretFile = join(tmpdir(), 'freya-e2e-totp-' + Buffer.from(base).toString('hex'))

function rememberSecret(secret: string): void {
  mkdirSync(tmpdir(), { recursive: true })
  writeFileSync(secretFile, secret, { mode: 0o600 })
}

const periodFile = secretFile + '.period'

function rememberPeriod(p: number): void {
  writeFileSync(periodFile, String(p), { mode: 0o600 })
}

function lastPeriod(): number {
  return existsSync(periodFile) ? Number(readFileSync(periodFile, 'utf8')) : -1
}

function storedSecret(): string {
  if (process.env.E2E_TOTP_SECRET) return process.env.E2E_TOTP_SECRET
  return existsSync(secretFile) ? readFileSync(secretFile, 'utf8').trim() : ''
}

export { base }
