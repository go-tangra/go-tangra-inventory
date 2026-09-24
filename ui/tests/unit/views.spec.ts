import { beforeEach, describe, expect, it, vi } from 'vitest'
import { createPinia, setActivePinia } from 'pinia'
import { flushPromises, mount } from '@vue/test-utils'
import { createRouter, createMemoryHistory } from 'vue-router'
import Hosts from '@/views/hosts/index.vue'
import Detail from '@/views/hosts/detail.vue'
import Agents from '@/views/agents/index.vue'
import Dashboard from '@/views/dashboard/index.vue'
import { enrollTokenSchema, hostFilterSchema } from '@/schemas'

function fetchMock(handler: (url: string, init: RequestInit) => unknown) {
  const calls: { url: string; init: RequestInit }[] = []
  vi.stubGlobal('fetch', vi.fn(async (url: string, init: RequestInit = {}) => {
    calls.push({ url, init })
    const body = handler(url, init)
    if (body === 204) return new Response(null, { status: 204 })
    return new Response(JSON.stringify(body), { status: 200, headers: { 'Content-Type': 'application/json' } })
  }))
  return calls
}
class FakeSource { onopen = null; onerror = null; addEventListener() {} close() {} }
const router = createRouter({ history: createMemoryHistory(), routes: [{ path: '/', component: { template: '<div/>' } }, { path: '/inventory', name: 'inventory-hosts', component: { template: '<div/>' } }, { path: '/inventory/host/:id', name: 'inventory-host', component: { template: '<div/>' } }] })
const global = { plugins: [router] }
const host = { id: 'h1', hostname: 'pc-01', status: 'active', os_name: 'Windows', os_version: '11', manufacturer: 'Dell', model: 'XPS', first_seen: '2026-01-01T00:00:00Z', last_seen: '2026-01-02T00:00:00Z', tags: { site: 'hq' }, system_serial: 'SN1' }
const snapshot = { id: 'snap1234abcd', host_id: 'h1', collected_at: '2026-01-02T00:00:00Z', received_at: '2026-01-02T00:00:01Z', source: 'agent', payload: { os: { name: 'Windows' }, bios: { vendor: 'Dell' }, system: {}, baseboard: {}, chassis: {}, memory: { array: {}, modules: [{ device_locator: 'DIMM0', capacity_bytes: 8589934592 }] }, processors: [{ socket_designation: 'CPU0', core_count: 8 }], environment: {}, network_interfaces: [{ name: 'eth0', up: true, ip_addresses: ['10.0.0.5'] }] } }

describe('inventory views on the kit', () => {
  beforeEach(() => {
    setActivePinia(createPinia())
    document.cookie = '__Host-csrf=tok; Secure; Path=/'
    vi.stubGlobal('EventSource', FakeSource)
    ;(globalThis as unknown as { __vw: number }).__vw = 1280
  })

  it('schemas: enrol label optional, filter tag must be key or key=value', () => {
    expect(enrollTokenSchema.parse({ label: '' })).toEqual({})
    expect(enrollTokenSchema.parse({ label: ' lab ' })).toEqual({ label: 'lab' })
    expect(hostFilterSchema.safeParse({ tag: 'site=hq' }).success).toBe(true)
    expect(hostFilterSchema.safeParse({ tag: 'a=b=c' }).success).toBe(false)
    expect(hostFilterSchema.safeParse({ status: 'gone' }).success).toBe(false)
  })

  it('hosts list: rows with status/agent chips, filter submits validated query, no inline styles', async () => {
    const calls = fetchMock((url) => (url.includes('/hosts') ? { items: [host] } : { items: [{ agent_id: 'ag1', host_id: 'h1' }] }))
    const w = mount(Hosts, { global })
    await flushPromises()
    expect(w.find('[data-test="host-row-h1"]').exists()).toBe(true)
    expect(w.find('[data-test="host-online-h1"]').exists()).toBe(true)
    expect(w.find('[style]').exists()).toBe(false)
    const tag = w.find('input[data-field=tag]')
    await tag.setValue('a=b=c')
    await w.find('form').trigger('submit')
    await flushPromises()
    expect(calls.filter((c) => c.url.includes('/hosts')).length).toBe(1) // blocked by validation
    expect(w.find('[role=alert]').text()).toContain('key=value')
    await tag.setValue('site=hq')
    await w.find('form').trigger('submit')
    await flushPromises()
    expect(calls.at(-1)!.url).toContain('tag=site%3Dhq')
    w.unmount()
  })

  it('host detail: summary key/value table, hardware tables, tabs switch, history compare', async () => {
    await router.push('/inventory/host/h1')
    fetchMock((url) => (url.endsWith('/hosts/h1') ? host : url.endsWith('/latest') ? snapshot : url.includes('/snapshots') ? { items: [snapshot] } : url.includes('/changes') ? { items: [] } : { items: [] }))
    const w = mount(Detail, { global })
    await flushPromises()
    expect(w.find('h1').text()).toBe('pc-01')
    expect(w.find('dl').text()).toContain('Dell')
    expect(w.text()).toContain('DIMM0')
    expect(w.text()).toContain('8.0 GB')
    await w.findAll('[role=tab]')[2]!.trigger('click')
    await flushPromises()
    expect(w.text()).toContain('10.0.0.5')
    await w.findAll('[role=tab]')[3]!.trigger('click')
    await flushPromises()
    expect(w.find('[data-test=diff-compare]').attributes('disabled')).toBeDefined()
    expect(w.find('[data-test=snapshots-table]').text()).toContain('snap1234')
    w.unmount()
  })

  it('agents: enrol dialog shows the one-time token in a secret field with copy, then forgets it', async () => {
    fetchMock((url, init) => (init.method === 'POST' && url.endsWith('/agents/enroll-token') ? { id: 't1', token: 'SECRET-TOKEN-VALUE', expires_at: '2026-02-01T00:00:00Z' } : { items: [{ agent_id: 'agent-1234567890', host_id: 'h1', hostname: 'pc-01', version: '1.0' }] }))
    const w = mount(Agents, { global, attachTo: document.body })
    await flushPromises()
    expect(w.find('[data-test="agent-row-agent-1234567890"]').exists()).toBe(true)
    await w.find('[data-test=issue-token]').trigger('click')
    await flushPromises()
    const dialog = document.body.querySelector('[role=dialog]')!
    ;(dialog.querySelector('[data-test=enroll-mint]') as HTMLButtonElement).click()
    await flushPromises()
    const secret = dialog.querySelector<HTMLInputElement>('input[type=password]')!
    expect(secret.value).toBe('SECRET-TOKEN-VALUE')
    expect(secret.getAttribute('autocomplete')).toBe('off')
    expect(dialog.querySelector('button[aria-label="Copy token"]')).toBeTruthy()
    expect(localStorage.length).toBe(0)
    ;(Array.from(dialog.querySelectorAll('button')).find((b) => b.textContent?.trim() === 'Done') as HTMLButtonElement).click()
    await flushPromises()
    expect(document.body.textContent).not.toContain('SECRET-TOKEN-VALUE')
    w.unmount()
  })

  it('dashboard: stat tiles and bar lists', async () => {
    fetchMock((url) => (url.includes('/statistics') ? { hosts_total: 3, agents_online: 1, hosts_by_status: { active: 2, stale: 1 }, hosts_by_os: { Windows: 3 }, hosts_by_manufacturer: {} } : url.includes('/hosts') ? { items: [] } : { items: [] }))
    const w = mount(Dashboard, { global })
    await flushPromises()
    expect(w.findAll('.stat-tile').length).toBe(7)
    expect(w.findAll('progress').length).toBe(3)
    expect(w.text()).toContain('No hosts yet')
    w.unmount()
  })
})
