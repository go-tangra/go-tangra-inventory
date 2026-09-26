import { beforeEach, describe, expect, it, vi } from 'vitest'
import { createPinia, setActivePinia } from 'pinia'
import { flushPromises, mount } from '@vue/test-utils'
import { createRouter, createMemoryHistory } from 'vue-router'
import Detail from '@/views/hosts/detail.vue'

class FakeSource { onopen = null; onerror = null; addEventListener() {} close() {} }
const router = createRouter({ history: createMemoryHistory(), routes: [{ path: '/', component: { template: '<div/>' } }, { path: '/inventory', name: 'inventory-hosts', component: { template: '<div/>' } }, { path: '/inventory/host/:id', name: 'inventory-host', component: { template: '<div/>' } }] })
const global = { plugins: [router] }
const host = { id: 'h1', hostname: 'srv-01', status: 'active', first_seen: '2026-01-01T00:00:00Z', last_seen: '2026-01-02T00:00:00Z' }
const base = { os: { name: 'Ubuntu', family: 'linux' }, bios: {}, system: {}, baseboard: {}, chassis: {}, memory: { array: {} }, environment: {} }

function mockFetch(payload: Record<string, unknown>) {
  const snapshot = { id: 'snap1', host_id: 'h1', collected_at: '2026-01-02T00:00:00Z', received_at: '2026-01-02T00:00:01Z', source: 'agent', payload: { ...base, ...payload } }
  vi.stubGlobal('fetch', vi.fn(async (url: string) => {
    const body = url.endsWith('/hosts/h1') ? host : url.endsWith('/latest') ? snapshot : { items: [] }
    return new Response(JSON.stringify(body), { status: 200, headers: { 'Content-Type': 'application/json' } })
  }))
}

async function openTab(index: number, payload: Record<string, unknown>) {
  await router.push('/inventory/host/h1')
  mockFetch(payload)
  const w = mount(Detail, { global })
  await flushPromises()
  await w.findAll('[role=tab]')[index]!.trigger('click')
  await flushPromises()
  return w
}

describe('host detail: host report data', () => {
  beforeEach(() => {
    setActivePinia(createPinia())
    vi.stubGlobal('EventSource', FakeSource)
  })

  it('network: kind, speed, gateway, DHCP and per-address flags, primary addresses, virtualization', async () => {
    const w = await openTab(2, {
      primary_ipv4: '192.0.2.10', primary_ipv6: '2001:db8::10',
      virtualization: { role: 'vm', kind: 'kvm', source: 'dmi' },
      network_interfaces: [{
        name: 'bond0', mac: '00:11:22:33:44:55', type: 'bond', speed_bps: 2000000000, gateway: '192.0.2.1', dhcp: true, up: true, default_route: true, vlan_id: 0,
        addresses: [
          { address: '192.0.2.10', prefix_length: 24, family: 'ipv4', dhcp: true, scope: 'global' },
          { address: '2001:db8::beef', prefix_length: 64, family: 'ipv6', temporary: true, deprecated: true, scope: 'global' },
        ],
      }],
    })
    const text = w.text()
    expect(text).toContain('bond')
    expect(text).toContain('2 Gbps')
    expect(text).toContain('192.0.2.1')
    expect(text).toContain('192.0.2.10/24')
    expect(text).toContain('2001:db8::beef/64')
    expect(w.find('[data-test=addr-flag-temporary]').exists()).toBe(true)
    expect(w.find('[data-test=addr-flag-deprecated]').exists()).toBe(true)
    expect(w.find('[data-test=iface-dhcp]').exists()).toBe(true)
    expect(text).toContain('2001:db8::10')
    expect(text).toContain('kvm')
    w.unmount()
  })

  it('network: old agent rows fall back to CIDR strings; BMC and guests sections', async () => {
    const w = await openTab(2, {
      network_interfaces: [{ name: 'eth0', ip_addresses: ['10.0.0.5/24'], up: true }],
      bmc: { address: '10.9.0.5', prefix_length: 24, gateway: '10.9.0.1', ip_source: 'static', vlan_id: 12, ports: [{ channel: 1, mac: '00:aa:bb:cc:dd:ee', address: '10.9.0.5' }] },
      hypervisor_guests: [{ id: '100', name: 'web-01', kind: 'vm', platform: 'proxmox', macs: ['bc:24:11:00:00:01'] }],
      truncated: { interfaces: 3 },
    })
    const text = w.text()
    expect(text).toContain('10.0.0.5/24')
    expect(w.find('[data-test=bmc-card]').text()).toContain('10.9.0.5/24')
    expect(w.find('[data-test=bmc-card]').text()).toContain('00:aa:bb:cc:dd:ee')
    expect(w.find('[data-test=guests-card]').text()).toContain('web-01')
    expect(w.find('[data-test=guests-card]').text()).toContain('bc:24:11:00:00:01')
    expect(w.find('[data-test=truncated-notice]').text()).toContain('3 interfaces')
    w.unmount()
  })

  it('software: update state with pending and security updates; unknown is never up to date', async () => {
    const w = await openTab(1, {
      update_state: { package_manager: 'apt', status: 'updates_available', reboot_required: 'true', automatic_updates: 'false', pending_count: 2, security_count: 1, checked_at: '2026-01-02T00:00:00Z' },
      installed_programs: [{ name: 'openssl', version: '3.0.1', available_version: '3.0.2', security_update: true }, { name: 'curl', version: '8.0' }],
    })
    const card = w.find('[data-test=update-card]')
    expect(card.text()).toContain('updates available')
    expect(card.text()).toContain('reboot required')
    expect(card.text()).toContain('apt')
    expect(w.text()).toContain('3.0.2')
    expect(w.find('[data-test=security-update]').exists()).toBe(true)
    w.unmount()

    const u = await openTab(1, {})
    expect(u.find('[data-test=update-card]').text()).toContain('unknown')
    expect(u.find('[data-test=update-card]').text()).not.toContain('up to date')
    u.unmount()
  })
})
