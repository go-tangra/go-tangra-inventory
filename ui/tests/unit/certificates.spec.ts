import { beforeEach, describe, expect, it, vi } from 'vitest'
import { createPinia, setActivePinia } from 'pinia'
import { flushPromises, mount } from '@vue/test-utils'
import { createRouter, createMemoryHistory } from 'vue-router'
import { abilitiesPlugin } from '@casl/vue'
import { createMongoAbility } from '@casl/ability'
import { useConfirm } from '@go-tangra/ui'
import CertificatesTab from '@/views/hosts/CertificatesTab.vue'
import Agents from '@/views/agents/index.vue'
import { useLive } from '@/stores/live'
import { ACTIVE_STATES, typesMatchSchema } from '@/stores/certificates'
import { RELOAD_DEBOUNCE_MS, capabilityLabel, capabilityText, hookText, reasonText, shortFingerprint, stateLabel, triggerLabel } from '@/views/hosts/cert-text'

class FakeSource { onopen = null; onerror = null; addEventListener() {} close() {} }

const FP = 'ab'.repeat(32)
const queued = { id: 'q1', delivery_id: 'd1', host_id: 'h1', hostname: 'web-1', name: 'www', certificate_id: 'cert-2', configuration_id: 'cfg-1', trigger: 'auto_deploy', state: 'pending', reason: '', attempts: 1, serial: '', fingerprint_sha256: '', common_name: '', not_after: null, hook_exit_code: null, detail: '', created_at: '2026-10-01T09:00:00Z', updated_at: '2026-10-01T09:00:00Z', finished_at: null }
const done = { ...queued, id: 'i1', delivery_id: 'd0', certificate_id: 'cert-1', trigger: 'manual', state: 'installed', serial: '4f3a', fingerprint_sha256: FP, common_name: 'www.example.com', not_after: '2027-01-01T00:00:00Z', hook_exit_code: 0, finished_at: '2026-09-01T08:00:00Z', created_at: '2026-09-01T08:00:00Z' }
const failed = { ...queued, id: 'i2', name: 'mail', state: 'failed', reason: 'disk_full', hook_exit_code: -1, finished_at: '2026-09-02T08:00:00Z' }
const certs = [
  { name: 'api', certificate_id: 'cert-9', configuration_id: 'cfg-9', common_name: 'api.example.com', serial: '01', fingerprint_sha256: 'cd'.repeat(32), not_after: '2027-02-01T00:00:00Z', state: 'unchanged', reason: '', hook_exit_code: -1, last_delivered_at: '2026-09-03T08:00:00Z', revoked: true, revoked_at: '2026-09-20T08:00:00Z', active_item: null },
  { name: 'mail', certificate_id: 'cert-1', configuration_id: 'cfg-1', common_name: '', serial: '', fingerprint_sha256: '', not_after: null, state: 'failed', reason: 'write_failed', hook_exit_code: null, last_delivered_at: null, revoked: false, revoked_at: null, active_item: null },
  { name: 'smtp', certificate_id: 'cert-1', configuration_id: 'cfg-1', common_name: '<b>bold</b>', serial: '02', fingerprint_sha256: 'ef'.repeat(32), not_after: '2027-03-01T00:00:00Z', state: 'hook_failed', reason: 'hook_failed', hook_exit_code: 3, last_delivered_at: '2026-09-04T08:00:00Z', revoked: false, revoked_at: null, active_item: null },
  { name: 'www', certificate_id: 'cert-1', configuration_id: 'cfg-1', common_name: 'www.example.com', serial: '4f3a', fingerprint_sha256: FP, not_after: '2027-01-01T00:00:00Z', state: 'installed', reason: '', hook_exit_code: 0, last_delivered_at: '2026-09-01T08:00:00Z', revoked: false, revoked_at: null, active_item: queued },
]

function fetchMock(handler: (url: string, init: RequestInit) => unknown) {
  const calls: { url: string; init: RequestInit }[] = []
  vi.stubGlobal('fetch', vi.fn(async (url: string, init: RequestInit = {}) => {
    calls.push({ url, init })
    return new Response(JSON.stringify(handler(url, init)), { status: 200, headers: { 'Content-Type': 'application/json' } })
  }))
  return calls
}
const page = (items: unknown[], url: string, total = items.length) => {
  const q = new URL(url, 'https://x').searchParams
  return { items, total, page: Number(q.get('page') ?? 1), page_size: Number(q.get('page_size') ?? 25), sort: q.get('sort'), order: q.get('order') }
}
const handler = (url: string, init: RequestInit): unknown => {
  if (init.method === 'POST' && url.includes('/cancel')) return { ...queued, state: 'cancelled', reason: 'cancelled_by_user' }
  if (url.includes('/hosts/h1/certificates')) {
    const q = new URL(url, 'https://x').searchParams
    return page(q.get('revoked') === 'true' ? certs.filter((c) => c.revoked) : certs, url)
  }
  if (url.includes('/certificate-deliveries')) return page([queued, done, failed], url, 30)
  return { items: [] }
}

const router = createRouter({ history: createMemoryHistory(), routes: [{ path: '/inventory/host/:id', name: 'inventory-host', component: { template: '<div/>' } }, { path: '/inventory/agents', component: { template: '<div/>' } }] })
const manager = [{ action: 'read', subject: 'InventoryHost' }, { action: 'manage', subject: 'InventoryAgent' }]
const reader = [{ action: 'read', subject: 'InventoryHost' }]
function mountTab(rules: { action: string; subject: string }[]) {
  return mount(CertificatesTab, { props: { hostId: 'h1' }, global: { plugins: [router, [abilitiesPlugin, createMongoAbility(rules)]] as never[] }, attachTo: document.body })
}
const certCalls = (calls: { url: string }[]) => calls.filter((c) => c.url.includes('/hosts/h1/certificates'))
const historyCalls = (calls: { url: string }[]) => calls.filter((c) => c.url.includes('/certificate-deliveries?'))

describe('host certificates tab', () => {
  beforeEach(async () => {
    setActivePinia(createPinia())
    document.cookie = '__Host-csrf=tok; Secure; Path=/'
    vi.stubGlobal('EventSource', FakeSource)
    ;(globalThis as unknown as { __vw: number }).__vw = 1280
    await router.push('/inventory/host/h1')
  })

  it('renders one row per name with state badges, reasons, hook results, revoked badge and expiry in the locale', async () => {
    fetchMock(handler)
    const w = mountTab(reader)
    await flushPromises()
    const www = w.find('[data-test="cert-row-www"]')
    expect(www.text()).toContain('installed')
    expect(www.text()).toContain('www.example.com')
    expect(www.text()).toContain(new Date('2027-01-01T00:00:00Z').toLocaleString())
    expect(www.text()).toContain(new Date('2026-09-01T08:00:00Z').toLocaleString())
    expect(www.find('[data-test="cert-queued-www"]').text()).toContain('queued')
    expect(w.find('[data-test="cert-row-api"]').text()).toContain('unchanged')
    expect(w.find('[data-test="cert-revoked-api"]').text()).toBe('revoked')
    expect(w.find('[data-test="cert-revoked-www"]').exists()).toBe(false)
    // Failure reasons render as text, never as the raw code.
    expect(w.find('[data-test="cert-reason-mail"]').text()).toBe(reasonText('write_failed'))
    expect(w.find('[data-test="cert-row-mail"]').text()).not.toContain('write_failed')
    expect(w.find('[data-test="cert-row-smtp"]').text()).toContain('hook failed')
    expect(w.find('[data-test="cert-hook-smtp"]').text()).toBe('exit 3')
    expect(w.find('[data-test="cert-hook-www"]').text()).toBe('ok (0)')
    w.unmount()
  })

  it('shortens fingerprints with a copy button, links the deployer configuration and renders strings as text', async () => {
    fetchMock(handler)
    const w = mountTab(reader)
    await flushPromises()
    const www = w.find('[data-test="cert-row-www"]')
    expect(www.find('[data-test="cert-fp-www"]').text()).toBe(shortFingerprint(FP))
    expect(w.text()).not.toContain(FP)
    expect(www.find('button[aria-label="Copy fingerprint"]').exists()).toBe(true)
    expect(www.find('[data-test="cert-deployer-www"]').attributes('href')).toBe('/deployer/configurations?id=cfg-1')
    // A common name with markup is shown as text.
    expect(w.find('[data-test="cert-row-smtp"] b').exists()).toBe(false)
    expect(w.find('[data-test="cert-row-smtp"]').text()).toContain('<b>bold</b>')
    expect(w.html()).not.toContain('-----BEGIN')
    w.unmount()
  })

  it('pages and sorts the delivery history on the server', async () => {
    const calls = fetchMock(handler)
    const w = mountTab(reader)
    await flushPromises()
    expect(certCalls(calls)[0]!.url).toBe('/api/inventory/v1/hosts/h1/certificates?page=1&page_size=25&sort=name&order=asc')
    expect(historyCalls(calls)[0]!.url).toBe('/api/inventory/v1/certificate-deliveries?host_id=h1&page=1&page_size=25&sort=created_at&order=desc')
    const history = w.find('[data-test="deliveries-table"]')
    expect(history.text()).toContain('queued')
    expect(history.find('[data-test="delivery-reason-i2"]').text()).toBe(reasonText('disk_full'))
    expect(history.text()).toContain('auto deploy')
    w.unmount()
    // The page lives in the URL (?deliveries.page=2).
    await router.push('/inventory/host/h1?deliveries.page=2&deliveries.sort=state')
    const calls2 = fetchMock(handler)
    const w2 = mountTab(reader)
    await flushPromises()
    const u = new URL(historyCalls(calls2)[0]!.url, 'https://x').searchParams
    expect([u.get('page'), u.get('sort'), u.get('host_id')]).toEqual(['2', 'state', 'h1'])
    w2.unmount()
  })

  it('filters revoked certificates on the server', async () => {
    const calls = fetchMock(handler)
    const w = mountTab(reader)
    await flushPromises()
    await w.find('[data-test="cert-revoked-filter"] select').setValue('true')
    await flushPromises()
    expect(new URL(certCalls(calls).at(-1)!.url, 'https://x').searchParams.get('revoked')).toBe('true')
    expect(w.find('[data-test="cert-row-api"]').exists()).toBe(true)
    expect(w.find('[data-test="cert-row-www"]').exists()).toBe(false)
    w.unmount()
  })

  it('offers cancel on queued deliveries only with manage InventoryAgent', async () => {
    fetchMock(handler)
    const r = mountTab(reader)
    await flushPromises()
    expect(r.find('[data-test="cert-cancel-q1"]').exists()).toBe(false)
    expect(r.find('[data-test="delivery-cancel-q1"]').exists()).toBe(false)
    r.unmount()

    const calls = fetchMock(handler)
    const w = mountTab(manager)
    await flushPromises()
    expect(w.find('[data-test="cert-cancel-q1"]').exists()).toBe(true)
    expect(w.find('[data-test="delivery-cancel-q1"]').exists()).toBe(true)
    expect(w.find('[data-test="delivery-cancel-i1"]').exists()).toBe(false) // final items are not cancellable
    const before = certCalls(calls).length
    await w.find('[data-test="cert-cancel-q1"]').trigger('click')
    await flushPromises()
    useConfirm().answer(true)
    await flushPromises()
    const post = calls.find((c) => c.init.method === 'POST')
    expect(post?.url).toBe('/api/inventory/v1/certificate-deliveries/q1/cancel')
    expect(certCalls(calls).length).toBeGreaterThan(before)
    expect(w.find('[data-test="cert-message"]').text()).toContain('cancelled')
    w.unmount()
  })

  it('reloads (debounced) on live delivery events of this host only', async () => {
    const calls = fetchMock(handler)
    const w = mountTab(reader)
    await flushPromises()
    const n = certCalls(calls).length
    const live = useLive()
    live._emit('inventory.certificate.delivery', JSON.stringify({ host_id: 'h2', item_id: 'x', state: 'installed' }))
    for (let i = 0; i < 3; i++) live._emit('inventory.certificate.delivery', JSON.stringify({ host_id: 'h1', item_id: 'q1', state: 'delivered' }))
    await new Promise((r) => setTimeout(r, RELOAD_DEBOUNCE_MS + 50))
    await flushPromises()
    expect(certCalls(calls).length).toBe(n + 1)
    expect(historyCalls(calls).length).toBe(n + 1)
    w.unmount()
  })
})

describe('agent list certificate column', () => {
  beforeEach(() => {
    setActivePinia(createPinia())
    document.cookie = '__Host-csrf=tok; Secure; Path=/'
    vi.stubGlobal('EventSource', FakeSource)
    ;(globalThis as unknown as { __vw: number }).__vw = 1280
  })

  it('shows the capability badge with its reason as tooltip', async () => {
    const items = [
      { agent_id: 'a1', host_id: 'h1', hostname: 'web-1', version: '4.7.0', online: true, upgrade_state: 'up_to_date', certificate_capability: 'enabled' },
      { agent_id: 'a2', host_id: 'h2', hostname: 'web-2', version: '4.6.0', online: true, upgrade_state: 'available', certificate_capability: 'upgrade_required' },
      { agent_id: 'a3', host_id: 'h3', hostname: 'win-1', version: '4.7.0', online: false, upgrade_state: 'up_to_date', certificate_capability: 'not_supported_platform' },
      { agent_id: 'a4', host_id: 'h4', hostname: 'web-4', version: '4.7.0', online: true, upgrade_state: 'up_to_date', certificate_capability: 'disabled_on_host' },
    ]
    fetchMock((url) => (url.includes('/upgrade-policy') ? {} : url.includes('/auto-enroll') ? { enabled: false, keys: [] } : { items, total: items.length, page: 1, page_size: 25 }))
    const w = mount(Agents, { global: { plugins: [router, [abilitiesPlugin, createMongoAbility([{ action: 'manage', subject: 'InventoryAgent' }])]] as never[] }, attachTo: document.body })
    await flushPromises()
    for (const a of items) {
      const cell = w.find(`[data-test="agent-cert-${a.agent_id}"]`)
      expect(cell.text()).toBe(capabilityLabel(a.certificate_capability))
      expect(cell.attributes('data-tip')).toBe(capabilityText(a.certificate_capability))
    }
    w.unmount()
  })
})

describe('certificate text helpers', () => {
  it('maps codes to text and keeps unknown codes', () => {
    expect(stateLabel('pending')).toBe('queued')
    expect(stateLabel('mystery')).toBe('mystery')
    expect(reasonText('')).toBe('')
    expect(reasonText('new_code')).toBe('new_code')
    expect(hookText(null)).toBe('')
    expect(hookText(-1)).toBe('not run')
    expect(hookText(256)).toBe('timed out')
    expect(shortFingerprint('')).toBe('')
    expect(shortFingerprint('abc')).toBe('abc')
    expect(capabilityLabel('x')).toBe('x')
    expect(capabilityText('x')).toBe('x')
    expect(triggerLabel('auto_deploy')).toBe('auto deploy')
    expect(triggerLabel('x')).toBe('x')
    expect([...ACTIVE_STATES]).toEqual(['pending', 'delivered', 'fetched'])
    expect(typesMatchSchema).toEqual([true, true])
  })
})
