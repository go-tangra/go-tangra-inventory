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

const node1Memory = {
  total_physical_bytes: 17179869184, slots_total: 2, slots_populated: 1,
  array: { location: 'System board or motherboard', use: 'System memory', error_correction: 'Single-bit ECC', maximum_capacity: 13194139533312, number_of_devices: 2, handle: 64 },
  arrays: [{ location: 'System board or motherboard', use: 'System memory', error_correction: 'Single-bit ECC', maximum_capacity: 13194139533312, number_of_devices: 2, handle: 64 }],
  modules: [
    { device_locator: 'P1-DIMMA1', bank_locator: 'P0_Node0_Channel0_Dimm0', capacity_bytes: 17179869184, form_factor: 'DIMM', memory_type: 'DDR4', speed_mt_s: 3200, configured_speed_mt_s: 2666, manufacturer: 'Samsung', part_number: 'M393A2K43EB3-CWE', populated: true, type_detail: ['Synchronous', 'Registered (Buffered)'], rank_count: 2 },
    { device_locator: 'P1-DIMMB1', bank_locator: 'P0_Node0_Channel1_Dimm0' },
  ],
}

describe('host detail: hardware (feature 023)', () => {
  beforeEach(() => {
    setActivePinia(createPinia())
    vi.stubGlobal('EventSource', FakeSource)
  })

  it('memory: arrays, populated/total slots, empty slots, type detail, rated and configured speed', async () => {
    const w = await openTab(0, { hardware_schema: 2, memory: node1Memory })
    const card = w.find('[data-test=memory-card]')
    expect(card.text()).toContain('1 / 2 slots')
    expect(card.text()).toContain('System board or motherboard')
    expect(card.text()).toContain('System memory')
    expect(card.text()).toContain('Single-bit ECC')
    expect(card.text()).toContain('12.0 TB')
    expect(card.text()).toContain('DDR4')
    expect(card.text()).toContain('Registered (Buffered)')
    expect(card.text()).toContain('3200 (configured 2666)')
    expect(card.text()).toContain('M393A2K43EB3-CWE')
    const empty = w.findAll('[data-test=slot-empty]')
    expect(empty.length).toBe(1)
    expect(w.find('[data-test=legacy-hardware]').exists()).toBe(false)
    w.unmount()
  })

  it('chassis type, boot-up state and processor family/socket', async () => {
    const w = await openTab(0, {
      hardware_schema: 2,
      chassis: { manufacturer: 'Supermicro', type: 'Rack Mount Chassis', bootup_state: 'Safe' },
      processors: [{ socket_designation: 'CPU1', version: 'Intel(R) Xeon(R) Silver 4310 CPU @ 2.10GHz', family: 'Intel Xeon processor', upgrade: 'Socket LGA4189', core_count: 12, thread_count: 24 }],
    })
    const text = w.text()
    expect(text).toContain('Rack Mount Chassis')
    expect(text).toContain('Safe')
    expect(text).toContain('Intel Xeon processor')
    expect(text).toContain('Socket LGA4189')
    w.unmount()
  })

  it('legacy agents get a notice and their modules count as populated', async () => {
    const w = await openTab(0, { memory: { total_physical_bytes: 8589934592, array: { use: 'Video memory' }, modules: [{ device_locator: 'DIMM_A1', capacity_bytes: 8589934592, memory_type: 'LPDDR3' }] } })
    expect(w.find('[data-test=legacy-hardware]').text()).toContain('upgrade the agent')
    expect(w.findAll('[data-test=slot-empty]').length).toBe(0)
    w.unmount()
  })

  it('reported strings are rendered as text, never as HTML', async () => {
    const w = await openTab(0, { hardware_schema: 2, bios: { vendor: '<img src=x onerror=alert(1)>' }, memory: { array: {}, modules: [{ device_locator: '<b>A1</b>', populated: true }] } })
    expect(w.find('img').exists()).toBe(false)
    expect(w.find('b').exists()).toBe(false)
    expect(w.text()).toContain('<img src=x onerror=alert(1)>')
    expect(w.text()).toContain('<b>A1</b>')
    w.unmount()
  })
})

describe('host detail: disks and filesystems (feature 023)', () => {
  beforeEach(() => {
    setActivePinia(createPinia())
    vi.stubGlobal('EventSource', FakeSource)
  })

  it('disks table with media, interface and removable badge; filesystems with disks and usage', async () => {
    const w = await openTab(0, {
      hardware_schema: 2,
      disks: [
        { name: 'nvme0n1', model: 'SAMSUNG MZQL23T8HCLS-00A07', serial: 'S64HNE0T000001', size_bytes: 3840755982336, media_type: 'nvme_ssd', interface: 'nvme' },
        { name: 'sdb', model: 'ST4000NM000A-2HZ', serial: 'ZC100003', size_bytes: 4000787030016, media_type: 'hdd', interface: 'sata' },
        { name: 'sdc', model: 'Ultra Fit', vendor: 'SanDisk', size_bytes: 30752000000, media_type: 'unknown', interface: 'usb', removable: true },
      ],
      filesystems: [
        { mount: '/', fs: 'ext4', device: '/dev/mapper/vg0-root', size_bytes: 1000, free_bytes: 250, disks: ['nvme0n1'] },
        { mount: '/srv', fs: 'xfs', device: '/dev/md0', size_bytes: 2000, free_bytes: 2000, disks: ['sda', 'sdb'] },
      ],
    })
    const disks = w.find('[data-test=disks-card]')
    expect(disks.text()).toContain('nvme0n1')
    expect(disks.text()).toContain('SAMSUNG MZQL23T8HCLS-00A07')
    expect(disks.text()).toContain('S64HNE0T000001')
    expect(disks.text()).toContain('3.5 TB')
    expect(disks.text()).toContain('NVMe SSD')
    expect(disks.text()).toContain('HDD')
    expect(disks.text()).toContain('SATA')
    expect(w.findAll('[data-test=disk-removable]').length).toBe(1)
    const fs = w.find('[data-test=filesystems-card]')
    expect(fs.text()).toContain('/dev/mapper/vg0-root')
    expect(fs.text()).toContain('sda, sdb')
    expect(fs.text()).toContain('75%')
    w.unmount()
  })

  it('legacy agents keep their partition groups', async () => {
    const w = await openTab(0, { disks: [{ partitions: [{ mount: '/', fs: 'ext4', size_bytes: 1024, free_bytes: 512 }] }] })
    expect(w.find('[data-test=disks-card]').exists()).toBe(false)
    expect(w.text()).toContain('Partitions')
    w.unmount()
  })
})
