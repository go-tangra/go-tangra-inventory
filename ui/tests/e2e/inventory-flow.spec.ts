import { expect, test, type Page } from '@playwright/test'
import { base, signIn } from '../../../../gateway/shell/tests/e2e/helpers'

// Quickstart §4 flow for the inventory remote at the three reference widths.
// Needs a full platform; skips without operator credentials.
const password = process.env.E2E_OPERATOR_PASSWORD ?? ''
const email = process.env.E2E_OPERATOR_EMAIL ?? 'ops@example.org'
const viewports = [{ name: 'phone', width: 320, height: 640 }, { name: 'tablet', width: 768, height: 1024 }, { name: 'desktop', width: 1280, height: 800 }]

async function openNav(page: Page, group: string, entry: string): Promise<void> {
  const burger = page.getByRole('button', { name: 'Open navigation' })
  if (await burger.isVisible()) await burger.click()
  const g = page.getByTestId('nav-group-' + group)
  if ((await g.getAttribute('aria-expanded')) !== 'true') await g.click()
  await page.getByTestId('nav-' + group).filter({ hasText: entry }).first().click()
}

test.describe('inventory remote', () => {
  test.skip(!password, 'E2E_OPERATOR_PASSWORD not set')
  for (const vp of viewports) {
    test(`${vp.name}: hosts list, agents enrol token shown once, dashboard`, async ({ page }) => {
      await page.setViewportSize({ width: vp.width, height: vp.height })
      const violations: string[] = []
      await page.addInitScript(() => document.addEventListener('securitypolicyviolation', (e) => console.error('CSP:' + (e as SecurityPolicyViolationEvent).violatedDirective)))
      page.on('console', (m) => { if (m.text().startsWith('CSP:')) violations.push(m.text()) })
      await page.goto(base + '/')
      await signIn(page, email, password)
      await openNav(page, 'inventory', 'Hosts')
      await expect(page.locator('main h1')).toHaveText('Hosts')
      await expect(page.getByTestId('hosts-table')).toBeVisible()
      await openNav(page, 'inventory', 'Agents')
      await page.getByTestId('issue-token').click()
      await page.getByTestId('enroll-mint').click()
      const secret = page.getByRole('dialog').locator('input[type=password]')
      await expect(secret).toBeVisible()
      await expect(secret).toHaveAttribute('autocomplete', 'off')
      await page.getByRole('dialog').getByRole('button', { name: 'Done' }).click()
      await expect(page.getByRole('dialog')).toBeHidden()
      await openNav(page, 'inventory', 'Dashboard')
      await expect(page.locator('.stat-tile').first()).toBeVisible()
      expect(await page.evaluate(() => document.documentElement.scrollWidth - document.documentElement.clientWidth)).toBeLessThanOrEqual(0)
      expect(await page.locator('main [style]').count()).toBe(0)
      expect(violations).toEqual([])
    })
  }
})
