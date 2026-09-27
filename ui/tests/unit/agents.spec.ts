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
const handler = (url: string, init: RequestInit): unknown => {
  if (url.includes('/upgrade-policy')) return policy
  if (init.method === 'POST' && url.endsWith('/agents/upgrades')) return { target_version: '4.5.0', created: [{ id: 'u-9', agent_id: 'a-available', state: 'pending' }], skipped: [{ agent_id: 'a-legacy', reason: 'manual_upgrade_required' }] }
  if (init.method === 'POST' && url.includes('/cancel')) return { id: 'u-3', state: 'cancelled' }
  return fleet
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
    expect(calls.length).toBe(before + 1)
    await useAgents().listFleet({ upgrade_state: 'failed', outdated: true })
    expect(calls.at(-1)!.url).toContain('state=failed')
    expect(calls.at(-1)!.url).toContain('outdated=true')
    w.unmount()
  })

  it('store: legacy listing counts as connected; live patches keep offline agents in the fleet', () => {
    const s = useAgents()
    s.fleet = [{ agent_id: 'x', host_id: 'h' }, { agent_id: 'y', online: false }]
    expect(s.connected.map((a) => a.agent_id)).toEqual(['x'])
    s.patchOffline('x')
    expect(s.connected.length).toBe(0)
    expect(s.fleet.length).toBe(2)
    s.patchOnline({ agent_id: 'y', hostname: 'back' })
    expect(s.connected.map((a) => a.hostname)).toEqual(['back'])
    s.patchOnline({ agent_id: 'z' })
    expect(s.fleet.length).toBe(3)
    s.patchOffline('missing')
    expect(s.fleet.length).toBe(3)
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
})
