import { beforeEach, describe, expect, it, vi } from 'vitest'
import { createPinia, setActivePinia } from 'pinia'
import { flushPromises, mount } from '@vue/test-utils'
import { abilitiesPlugin } from '@casl/vue'
import { createMongoAbility } from '@casl/ability'
import Agents from '@/views/agents/index.vue'
import { useAgents } from '@/stores/agents'
import { useLive } from '@/stores/live'
import { reasonText, skipSummary, fleetStateLabel } from '@/views/agents/upgrade-text'
import { validTimezone } from '@/schemas'

class FakeSource { onopen = null; onerror = null; addEventListener() {} close() {} }

const fleet = {
  current_version: '4.5.0',
  items: [
    { agent_id: 'a-uptodate', host_id: 'h1', hostname: 'node-1', version: '4.5.0', online: true, upgrade_state: 'up_to_date', state_changed_at: '2026-09-01T00:00:00Z' },
    { agent_id: 'a-available', host_id: 'h2', hostname: 'node-2', version: '4.4.0', online: true, target_version: '4.5.0', upgrade_state: 'available' },
    { agent_id: 'a-pending', host_id: 'h3', hostname: 'node-3', version: '4.4.0', online: false, target_version: '4.5.0', upgrade_state: 'pending', upgrade_id: 'u-3' },
    { agent_id: 'a-rolled', host_id: 'h4', hostname: 'node-4', version: '4.4.0', online: true, target_version: '4.5.0', upgrade_state: 'rolled_back', upgrade_reason: 'start_timeout' },
    { agent_id: 'a-legacy', host_id: 'h5', hostname: 'old-1', version: '4.3.1', online: true, upgrade_state: 'manual_upgrade_required' },
  ],
}

function fetchMock(handler: (url: string, init: RequestInit) => unknown) {
  const calls: { url: string; init: RequestInit }[] = []
  vi.stubGlobal('fetch', vi.fn(async (url: string, init: RequestInit = {}) => {
    calls.push({ url, init })
    const body = handler(url, init)
    return new Response(JSON.stringify(body), { status: 200, headers: { 'Content-Type': 'application/json' } })
  }))
  return calls
}

function mountAs(rules: { action: string; subject: string }[]) {
  return mount(Agents, { global: { plugins: [[abilitiesPlugin, createMongoAbility(rules)]] as never[] }, attachTo: document.body })
}
const manager = [{ action: 'manage', subject: 'InventoryAgent' }]
const policy = { enabled: false, window_start: '02:00', window_end: '04:00', timezone: 'UTC', max_concurrent: 5, target_version: '', paused: false, paused_reason: '' }
const autoKey = { id: 'k-1', key_id: 'ak_0123456789abcdef01234567', name: 'lab', allowed_cidrs: ['10.0.0.0/8'], enabled: true, state: 'active', expires_at: null, max_enrollments: 10, enrollments: 3, last_used_at: null, last_used_ip: '', created_by: 'u1', created_at: '2026-09-01T00:00:00Z', updated_at: '2026-09-01T00:00:00Z' }
const autoEnroll = { enabled: false, window_seconds: 300, keys: [autoKey] }
const handler = (url: string, init: RequestInit): unknown => {
  if (url.includes('/upgrade-policy')) return policy
  if (url.includes('/auto-enroll')) return autoEnroll
  if (init.method === 'POST' && url.endsWith('/agents/upgrades')) return { target_version: '4.5.0', created: [{ id: 'u-9', agent_id: 'a-available', state: 'pending' }], skipped: [{ agent_id: 'a-legacy', reason: 'manual_upgrade_required' }] }
  if (init.method === 'POST' && url.includes('/cancel')) return { id: 'u-3', state: 'cancelled' }
  return fleetPage(url)
}
// A list contract server for GET /agents: state / outdated filters, the total.
function fleetPage(url: string): unknown {
  const q = new URL(url, 'https://x').searchParams
  let items = fleet.items
  if (q.get('state')) items = items.filter((a) => a.upgrade_state === q.get('state'))
  if (q.get('outdated') === 'true') items = items.filter((a) => a.version !== fleet.current_version)
  if (q.get('online') === 'true') items = items.filter((a) => a.online)
  return { ...fleet, items, total: items.length, page: 1, page_size: Number(q.get('page_size') ?? 25), sort: q.get('sort') ?? 'hostname', order: q.get('order') ?? 'asc' }
}

describe('agent fleet view', () => {
  beforeEach(() => {
    setActivePinia(createPinia())
    document.cookie = '__Host-csrf=tok; Secure; Path=/'
    vi.stubGlobal('EventSource', FakeSource)
    ;(globalThis as unknown as { __vw: number }).__vw = 1280
  })

  it('lists online and offline agents with version, target, state chip, reason text and last change', async () => {
    fetchMock(handler)
    const w = mountAs(manager)
    await flushPromises()
    for (const a of fleet.items) expect(w.find(`[data-test="agent-row-${a.agent_id}"]`).exists()).toBe(true)
    const pending = w.find('[data-test="agent-row-a-pending"]')
    expect(pending.text()).toContain('offline')
    expect(pending.text()).toContain('4.5.0')
    expect(w.find('[data-test="agent-row-a-available"]').text()).toContain('online')
    expect(w.find('[data-test="agent-state-a-rolled"]').text()).toBe('Rolled back')
    // Reason codes render as text, never as the raw code.
    expect(w.find('[data-test="agent-reason-a-rolled"]').text()).toBe(reasonText('start_timeout'))
    expect(w.text()).not.toContain('start_timeout')
    expect(w.find('[data-test="agent-row-a-uptodate"]').text()).toContain(new Date('2026-09-01T00:00:00Z').toLocaleString())
    expect(w.text()).toContain('Current agent version 4.5.0')
    // Pre-4.4.0 agents are explained.
    expect(w.find('[data-test="manual-upgrade-note"]').text()).toContain('older than 4.4.0')
    w.unmount()
  })

  it('offers Upgrade only on upgradable rows and Cancel only on pending ones', async () => {
    fetchMock(handler)
    const w = mountAs(manager)
    await flushPromises()
    expect(w.find('[data-test="agent-upgrade-a-available"]').exists()).toBe(true)
    expect(w.find('[data-test="agent-upgrade-a-rolled"]').exists()).toBe(true)
    expect(w.find('[data-test="agent-upgrade-a-uptodate"]').exists()).toBe(false)
    expect(w.find('[data-test="agent-upgrade-a-legacy"]').exists()).toBe(false)
    expect(w.find('[data-test="agent-upgrade-a-pending"]').exists()).toBe(false)
    expect(w.find('[data-test="agent-cancel-a-pending"]').exists()).toBe(true)
    expect(w.find('[data-test="agent-cancel-a-available"]').exists()).toBe(false)
    w.unmount()
  })

  it('Upgrade posts the agent id with CSRF and reports created and skipped agents', async () => {
    const calls = fetchMock(handler)
    const w = mountAs(manager)
    await flushPromises()
    await w.find('[data-test="agent-upgrade-a-available"]').trigger('click')
    await flushPromises()
    const post = calls.find((c) => c.init.method === 'POST')!
    expect(post.url).toContain('/api/inventory/v1/agents/upgrades')
    expect(JSON.parse(String(post.init.body))).toEqual({ agent_ids: ['a-available'] })
    expect(new Headers(post.init.headers).get('X-CSRF-Token')).toBe('tok')
    const msg = w.find('[data-test="upgrade-message"]').text()
    expect(msg).toContain('1 upgrade to 4.5.0 requested')
    expect(msg).toContain('1 skipped')
    w.unmount()
  })

  it('Upgrade selected and Upgrade all outdated send the right body; Cancel posts to the request', async () => {
    const calls = fetchMock(handler)
    const w = mountAs(manager)
    await flushPromises()
    const store = useAgents()
    const res = await store.upgradeAllOutdated()
    expect(res.target_version).toBe('4.5.0')
    expect(JSON.parse(String(calls.at(-1)!.init.body))).toEqual({ all_outdated: true })
    // Selection: rows that cannot upgrade have a disabled checkbox.
    const boxes = w.findAll('[data-test="agents-table"] tbody input[type=checkbox]')
    expect(boxes.length).toBe(fleet.items.length)
    expect(boxes.filter((b) => !(b.element as HTMLInputElement).disabled).length).toBe(2)
    await boxes.find((b) => !(b.element as HTMLInputElement).disabled)!.trigger('click')
    await flushPromises()
    await w.find('[data-test="upgrade-selected"]').trigger('click')
    await flushPromises()
    expect(JSON.parse(String(calls.filter((c) => c.init.method === 'POST').at(-1)!.init.body))).toEqual({ agent_ids: ['a-available'] })
    await w.find('[data-test="agent-cancel-a-pending"]').trigger('click')
    await flushPromises()
    expect(calls.some((c) => c.init.method === 'POST' && c.url.endsWith('/agents/upgrades/u-3/cancel'))).toBe(true)
    expect(w.find('[data-test="upgrade-message"]').text()).toBe('Upgrade cancelled.')
    w.unmount()
  })

  it('hides every action without {manage, InventoryAgent}', async () => {
    fetchMock(handler)
    const w = mountAs([{ action: 'read', subject: 'InventoryHost' }])
    await flushPromises()
    expect(w.find('[data-test="agent-row-a-available"]').exists()).toBe(true)
    expect(w.find('[data-test="upgrade-all"]').exists()).toBe(false)
    expect(w.find('[data-test="issue-token"]').exists()).toBe(false)
    expect(w.find('[data-test="agent-upgrade-a-available"]').exists()).toBe(false)
    expect(w.find('[data-test="agent-cancel-a-pending"]').exists()).toBe(false)
    expect(w.find('[data-test="agent-revoke-a-available"]').exists()).toBe(false)
    expect(w.find('[data-test="agents-table"] tbody input[type=checkbox]').exists()).toBe(false)
    w.unmount()
  })

  it('filters by state and refetches on content-free upgrade events', async () => {
    const calls = fetchMock(handler)
    const w = mountAs(manager)
    await flushPromises()
    const before = calls.length
    useLive()._emit('inventory.agent.upgrade', '{"agent_id":"a-available","state":"installing"}')
    await flushPromises()
    // The page and the two fleet-wide counts (manual installs, outdated).
    expect(calls.length).toBe(before + 3)
    await useAgents().listFleet({ upgrade_state: 'failed', outdated: true })
    expect(calls.at(-1)!.url).toContain('state=failed')
    expect(calls.at(-1)!.url).toContain('outdated=true')
    w.unmount()
  })

  it('store: every connected agent is read page by page; live patches keep offline agents on the fleet page', async () => {
    // 450 connected agents: three pages of 200.
    const calls = fetchMock((url) => {
      const q = new URL(url, 'https://x').searchParams
      const page = Number(q.get('page'))
      const size = Number(q.get('page_size'))
      const n = Math.max(0, Math.min(size, 450 - (page - 1) * size))
      return { items: Array.from({ length: n }, (_, i) => ({ agent_id: 'c' + ((page - 1) * size + i), online: true })), total: 450, page, page_size: size }
    })
    const s = useAgents()
    await s.listConnected()
    expect(calls.map((c) => new URL(c.url, 'https://x').searchParams.get('page'))).toEqual(['1', '2', '3'])
    expect(calls.every((c) => c.url.includes('online=true') && c.url.includes('page_size=200'))).toBe(true)
    expect(s.connected.length).toBe(450)

    s.connected = [{ agent_id: 'x', host_id: 'h', online: true }]
    s.fleet = [{ agent_id: 'x', host_id: 'h' }, { agent_id: 'y', online: false }]
    s.patchOffline('x')
    expect(s.connected.length).toBe(0)
    expect(s.fleet.length).toBe(2)
    expect(s.fleet[0]!.online).toBe(false)
    s.patchOnline({ agent_id: 'y', hostname: 'back' })
    expect(s.connected.map((a) => a.hostname)).toEqual(['back'])
    expect(s.fleet[1]!.online).toBe(true)
    // An agent not on the (server-ordered) page is not inserted into it.
    s.patchOnline({ agent_id: 'z' })
    expect(s.fleet.length).toBe(2)
    expect(s.connected.length).toBe(2)
    s.patchOffline('missing')
    expect(s.fleet.length).toBe(2)
  })

  it('server paging and sorting: pager with the total, whole-fleet sort, state filter back to page 1, fleet-wide counts', async () => {
    const calls = fetchMock((url, init) => {
      if (!url.includes('/agents?')) return handler(url, init)
      const q = new URL(url, 'https://x').searchParams
      if (q.get('page_size') === '1') return { items: [], total: q.get('outdated') ? 40 : q.get('state') ? 7 : 60, page: 1, page_size: 1 }
      const size = Number(q.get('page_size'))
      const page = Math.min(Number(q.get('page')), Math.ceil(60 / size))
      return { ...fleet, total: 60, page, page_size: size, sort: q.get('sort'), order: q.get('order') }
    })
    const w = mountAs(manager)
    await flushPromises()
    const lists = () => calls.filter((c) => c.url.includes('/agents?') && !c.url.includes('page_size=1&') && !c.url.endsWith('page_size=1'))
    const last = () => new URL(lists().at(-1)!.url, 'https://x').searchParams
    expect([last().get('page'), last().get('page_size'), last().get('sort'), last().get('order')]).toEqual(['1', '25', 'hostname', 'asc'])
    expect(w.text()).toContain('Showing 1–25 of 60')
    // Fleet-wide figures, not the visible page.
    expect(w.find('[data-test="manual-upgrade-note"]').text()).toContain('7 agents are older than 4.4.0')
    expect(w.find('[data-test="upgrade-all"]').attributes('disabled')).toBeUndefined()
    await w.find('[aria-label="Page 3"]').trigger('click')
    await flushPromises()
    expect(last().get('page')).toBe('3')
    const header = (label: string) => w.findAll('th button').find((b) => b.text().startsWith(label))
    for (const label of ['Hostname', 'Version', 'Upgrade', 'Last seen']) expect(header(label), label).toBeTruthy()
    await header('Upgrade')!.trigger('click')
    await flushPromises()
    expect([last().get('sort'), last().get('order'), last().get('page')]).toEqual(['state', 'asc', '1'])
    await header('Last seen')!.trigger('click')
    await flushPromises()
    expect([last().get('sort'), last().get('order')]).toEqual(['last_seen', 'desc'])
    await w.find('[aria-label="Page 2"]').trigger('click')
    await flushPromises()
    await w.find('[data-test="fleet-state-filter"] select').setValue('failed')
    await flushPromises()
    expect([last().get('state'), last().get('page'), last().get('sort')]).toEqual(['failed', '1', 'last_seen'])
    // The auto-enrollment keys are a server page too, sortable by creation.
    const keys = () => calls.filter((c) => c.url.includes('/agents/auto-enroll?'))
    expect(keys()[0]!.url).toBe('/api/inventory/v1/agents/auto-enroll?page=1&page_size=25&sort=name&order=asc')
    const card = w.find('[data-test=auto-enroll-keys]')
    await card.findAll('th button').find((b) => b.text().startsWith('Created'))!.trigger('click')
    await flushPromises()
    const k = new URL(keys().at(-1)!.url, 'https://x').searchParams
    expect([k.get('sort'), k.get('order'), k.get('page')]).toEqual(['created_at', 'desc', '1'])
    w.unmount()
  })

  it('policy card: read-only without {manage, InventoryAgentUpgradePolicy}; operators see no resume', async () => {
    fetchMock((url, init) => (url.includes('/upgrade-policy') ? { ...policy, paused: true, paused_reason: 'failed:u-1' } : handler(url, init)))
    const w = mountAs(manager)
    await flushPromises()
    expect(w.find('[data-test=policy-card]').exists()).toBe(true)
    expect(w.find('[data-test=policy-readonly]').exists()).toBe(true)
    expect(w.find('[data-test=policy-save]').exists()).toBe(false)
    expect(w.find('[data-test=policy-paused]').text()).toContain('paused')
    expect(w.find('[data-test=policy-resume]').exists()).toBe(false)
    for (const input of w.findAll('[data-test=policy-card] input')) expect(input.attributes('disabled')).toBeDefined()
    w.unmount()
  })

  it('policy card: an administrator saves a validated policy and resumes a paused one', async () => {
    let current = { ...policy, paused: true, paused_reason: 'rolled_back:u-2' }
    const calls = fetchMock((url, init) => {
      if (url.endsWith('/upgrade-policy/resume')) return (current = { ...current, paused: false, paused_reason: '' })
      if (url.endsWith('/upgrade-policy') && init.method === 'PUT') return (current = { ...current, ...JSON.parse(String(init.body)) })
      if (url.endsWith('/upgrade-policy')) return current
      return handler(url, init)
    })
    const w = mountAs([...manager, { action: 'manage', subject: 'InventoryAgentUpgradePolicy' }])
    await flushPromises()
    expect(w.find('[data-test=policy-readonly]').exists()).toBe(false)
    expect(w.find('[data-test=policy-paused]').text()).toContain('rolled back')
    const field = (name: string) => w.find(`[data-test=policy-card] input[data-field=${name}]`)
    // Invalid window and timezone are refused before any request.
    await field('window_start').setValue('25:00')
    await field('timezone').setValue('Mars/Olympus')
    await w.find('[data-test=policy-save]').trigger('click')
    await flushPromises()
    expect(calls.some((c) => c.init.method === 'PUT')).toBe(false)
    const alerts = w.find('[data-test=policy-card]').text()
    expect(alerts).toContain('HH:MM')
    expect(alerts).toContain('unknown timezone')
    await field('window_start').setValue('22:00')
    await field('window_end').setValue('02:00')
    await field('timezone').setValue('Europe/Sofia')
    await field('target_version').setValue('latest')
    await w.find('[data-test=policy-save]').trigger('click')
    await flushPromises()
    expect(calls.some((c) => c.init.method === 'PUT')).toBe(false)
    await field('target_version').setValue('4.5.0')
    await w.find('[data-test=policy-save]').trigger('click')
    await flushPromises()
    const put = calls.find((c) => c.init.method === 'PUT')!
    expect(JSON.parse(String(put.init.body))).toEqual({ enabled: false, window_start: '22:00', window_end: '02:00', timezone: 'Europe/Sofia', max_concurrent: 5, target_version: '4.5.0' })
    expect(new Headers(put.init.headers).get('X-CSRF-Token')).toBe('tok')
    expect(w.find('[data-test=policy-message]').text()).toBe('Upgrade policy saved.')
    await w.find('[data-test=policy-resume]').trigger('click')
    await flushPromises()
    expect(calls.some((c) => c.init.method === 'POST' && c.url.endsWith('/agents/upgrade-policy/resume'))).toBe(true)
    expect(w.find('[data-test=policy-paused]').exists()).toBe(false)
    w.unmount()
  })

  it('policy card: load and resume errors are shown', async () => {
    fetchMock((url, init) => {
      if (url.includes('/upgrade-policy')) throw new Error('boom')
      return handler(url, init)
    })
    const w = mountAs(manager)
    await flushPromises()
    expect(w.find('[data-test=policy-card] [role=alert]').exists()).toBe(true)
    w.unmount()
  })

  it('schema: timezone helper', () => {
    expect(validTimezone('Europe/Sofia')).toBe(true)
    expect(validTimezone('Local')).toBe(false)
    expect(validTimezone('')).toBe(false)
    expect(validTimezone('x'.repeat(65))).toBe(false)
  })

  it('text helpers: unknown codes pass through, skip summary groups reasons', () => {
    expect(reasonText('mystery')).toBe('mystery')
    expect(fleetStateLabel('in_progress')).toBe('Upgrading')
    expect(skipSummary([])).toBe('')
    expect(skipSummary([{ agent_id: 'a', reason: 'up_to_date' }, { agent_id: 'b', reason: 'up_to_date' }])).toBe(' 2 skipped: already on the target version (2).')
  })

  it('automatic enrollment card: keys, switch and one-time secret', async () => {
    let state = { ...autoEnroll }
    const calls = fetchMock((url, init) => {
      if (url.endsWith('/auto-enroll') && init.method === 'PUT') return (state = { ...state, ...JSON.parse(String(init.body)) })
      if (url.endsWith('/auto-enroll/keys') && init.method === 'POST') return { key: { ...autoKey, id: 'k-2', key_id: 'ak_ffffffffffffffffffffffff', name: 'office' }, secret: 'aks_one-time-secret' }
      if (url.endsWith('/auto-enroll')) return state
      return handler(url, init)
    })
    const w = mountAs(manager)
    await flushPromises()
    const card = w.find('[data-test="auto-enroll-card"]')
    expect(card.exists()).toBe(true)
    expect(w.find('[data-test="auto-key-ak_0123456789abcdef01234567"]').text()).toContain('3 / 10')
    expect(w.find('[data-test="auto-enroll-off"]').exists()).toBe(true)
    await w.find('[data-test="auto-enroll-enabled"] input').setValue(true)
    await flushPromises()
    expect(calls.some((c) => c.init.method === 'PUT' && c.url.endsWith('/agents/auto-enroll') && String(c.init.body).includes('true'))).toBe(true)
    w.unmount()
  })

  it('hides the automatic enrollment card from non-managers', async () => {
    fetchMock(handler)
    const w = mountAs([])
    await flushPromises()
    expect(w.find('[data-test="auto-enroll-card"]').exists()).toBe(false)
    w.unmount()
  })
})
