<script setup lang="ts">
import { computed, onMounted, onUnmounted, ref } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import { UiPage, UiAlert, UiCard, UiButton, UiBadge, UiStatusChip, UiLiveIndicator, UiKeyValueTable, UiDataTable, UiTabs, UiSelect, UiEmptyState, useConfirm, type Column, type KeyValue, type TabItem } from '@go-tangra/ui'
import { useHosts } from '@/stores/hosts'
import { useAgents } from '@/stores/agents'
import { useSnapshots } from '@/stores/snapshots'
import { useLive } from '@/stores/live'
import { describe } from '@/api/client'
import type { Change, Host, IfAddress, Snapshot, SnapshotDiff } from '@/api/types'

const route = useRoute()
const router = useRouter()
const hosts = useHosts()
const agents = useAgents()
const snapshots = useSnapshots()
const live = useLive()
const confirm = useConfirm()

const id = computed(() => String(route.params.id))
const host = ref<Host | null>(null)
const latest = ref<Snapshot | null>(null)
const changes = ref<Change[]>([])
const error = ref('')
const tab = ref('hardware')
const busy = ref(false)
const diffA = ref<string | undefined>()
const diffB = ref<string | undefined>()
const diff = ref<SnapshotDiff | null>(null)
const diffError = ref('')

let release: (() => void) | null = null
onMounted(() => {
  void load()
  void agents.listConnected()
  release = live.connect()
})
onUnmounted(() => release?.())

async function load(): Promise<void> {
  error.value = ''
  try {
    host.value = await hosts.get(id.value)
  } catch (e) {
    error.value = describe(e)
    return
  }
  try {
    latest.value = await hosts.latest(id.value)
  } catch {
    latest.value = null
  }
  void snapshots.listForHost(id.value)
  try {
    changes.value = await snapshots.changes(id.value)
  } catch {
    changes.value = []
  }
}

const online = computed(() => agents.connected.some((a) => a.host_id === id.value))
const inv = computed(() => latest.value?.payload ?? null)
const tabs: TabItem[] = [{ key: 'hardware', label: 'Hardware' }, { key: 'software', label: 'Software' }, { key: 'network', label: 'Network' }, { key: 'history', label: 'History' }]

function humanBytes(bytes?: number): string {
  if (!bytes) return ''
  const units = ['B', 'KB', 'MB', 'GB', 'TB', 'PB']
  const i = Math.floor(Math.log(bytes) / Math.log(1024))
  return (bytes / Math.pow(1024, i)).toFixed(i ? 1 : 0) + ' ' + units[i]
}
const fmt = (ts?: string): string => (ts ? new Date(ts).toLocaleString() : '')

async function retire(): Promise<void> {
  if (!(await confirm.ask({ title: 'Retire this host?', text: 'It stops receiving snapshots and is excluded from statistics.', confirmLabel: 'Retire' }))) return
  busy.value = true
  try {
    host.value = await hosts.retire(id.value)
  } catch (e) {
    error.value = describe(e)
  } finally {
    busy.value = false
  }
}
async function refresh(): Promise<void> {
  busy.value = true
  error.value = ''
  try {
    await agents.refresh(id.value)
  } catch (e) {
    error.value = describe(e)
  } finally {
    busy.value = false
  }
}
async function remove(): Promise<void> {
  if (!(await confirm.ask({ title: `Delete ${host.value?.hostname ?? 'this host'}?`, text: 'All snapshots and change history are removed.', danger: true, confirmLabel: 'Delete' }))) return
  busy.value = true
  try {
    await hosts.remove(id.value)
    void router.push({ name: 'inventory-hosts' })
  } catch (e) {
    error.value = describe(e)
    busy.value = false
  }
}

const summary = computed<KeyValue[]>(() => {
  const h = host.value
  if (!h) return []
  return [
    { label: 'Manufacturer', value: h.manufacturer }, { label: 'Model', value: h.model },
    { label: 'OS', value: [h.os_name, h.os_version, h.os_arch].filter(Boolean).join(' ') }, { label: 'Agent', value: h.agent_version },
    { label: 'Assigned user', value: h.assigned_user }, { label: 'Serial', value: h.system_serial, copyable: true },
    { label: 'First seen', value: fmt(h.first_seen) }, { label: 'Last seen', value: fmt(h.last_seen) },
  ]
})
const kv = (pairs: [string, unknown][]): KeyValue[] => pairs.map(([label, value]) => ({ label, value }))

// Row types for the section tables (index keys so UiDataTable can key rows).
type Row<T> = T & Record<string, unknown> & { id: string }
const rows = <T extends object>(list: T[] | undefined): Row<T>[] => (list ?? []).map((r, i) => ({ ...(r as T & Record<string, unknown>), id: String(i) }))
const processorColumns: Column<Row<NonNullable<NonNullable<typeof inv.value>['processors']>[number]>>[] = [
  { key: 'socket_designation', label: 'Socket' }, { key: 'manufacturer', label: 'Manufacturer' }, { key: 'version', label: 'Version' },
  { key: 'core_count', label: 'Cores', align: 'end' }, { key: 'thread_count', label: 'Threads', align: 'end', hideOnStack: true }, { key: 'max_speed_mhz', label: 'Max MHz', align: 'end', hideOnStack: true },
]
const memoryColumns: Column<Record<string, unknown>>[] = [
  { key: 'device_locator', label: 'Locator' }, { key: 'capacity_bytes', label: 'Capacity', format: (m) => humanBytes(m.capacity_bytes as number) }, { key: 'memory_type', label: 'Type' },
  { key: 'form_factor', label: 'Form factor', hideOnStack: true }, { key: 'speed_mt_s', label: 'Speed MT/s', align: 'end', hideOnStack: true }, { key: 'manufacturer', label: 'Manufacturer', hideOnStack: true },
]
const partitionColumns: Column<Record<string, unknown>>[] = [
  { key: 'mount', label: 'Mount' }, { key: 'fs', label: 'FS' }, { key: 'size_bytes', label: 'Size', align: 'end', format: (p) => humanBytes(p.size_bytes as number) }, { key: 'free_bytes', label: 'Free', align: 'end', format: (p) => humanBytes(p.free_bytes as number) },
]
const monitorColumns: Column<Record<string, unknown>>[] = [{ key: 'manufacturer', label: 'Manufacturer' }, { key: 'model', label: 'Model' }, { key: 'serial_number', label: 'Serial' }]
const programColumns: Column<Record<string, unknown>>[] = [{ key: 'name', label: 'Name', sortable: true }, { key: 'version', label: 'Version' }, { key: 'available_version', label: 'Update' }, { key: 'publisher', label: 'Publisher', hideOnStack: true }, { key: 'install_date', label: 'Installed', hideOnStack: true }]
const serviceColumns: Column<Record<string, unknown>>[] = [{ key: 'name', label: 'Name', sortable: true }, { key: 'display_name', label: 'Display name', hideOnStack: true }, { key: 'state', label: 'State', width: 'sm' }, { key: 'start_mode', label: 'Start mode', hideOnStack: true }]
const userColumns: Column<Record<string, unknown>>[] = [{ key: 'name', label: 'Name' }, { key: 'is_admin', label: 'Admin', format: (u) => (u.is_admin ? 'yes' : '') }, { key: 'last_logon', label: 'Last logon', format: (u) => fmt(u.last_logon as string) }]
const patchColumns: Column<Record<string, unknown>>[] = [{ key: 'id', label: 'ID', format: (p) => String(p.patch_id ?? '') }, { key: 'installed_on', label: 'Installed on' }]
const nicColumns: Column<Record<string, unknown>>[] = [
  { key: 'name', label: 'Name' }, { key: 'type', label: 'Kind', format: (n) => [n.type, n.vlan_id ? 'vlan ' + String(n.vlan_id) : '', n.master ? 'in ' + String(n.master) : ''].filter(Boolean).join(' · ') },
  { key: 'mac', label: 'MAC', hideOnStack: true }, { key: 'addresses', label: 'Addresses' },
  { key: 'speed_bps', label: 'Speed', align: 'end', hideOnStack: true, format: (n) => humanSpeed(n.speed_bps as number | undefined) },
  { key: 'gateway', label: 'Gateway', hideOnStack: true }, { key: 'dns', label: 'DNS', format: (n) => ((n.dns as string[] | undefined) ?? []).join(', '), hideOnStack: true }, { key: 'up', label: 'State', width: 'sm' },
]
// Per-address rows for new agents; older agents only report CIDR strings.
function addressesOf(n: Record<string, unknown>): { text: string; a?: IfAddress }[] {
  const list = n.addresses as IfAddress[] | undefined
  if (list?.length) return list.map((a) => ({ text: a.address + '/' + String(a.prefix_length), a }))
  return ((n.ip_addresses as string[] | undefined) ?? []).map((text) => ({ text }))
}
function humanSpeed(bps?: number): string {
  if (!bps) return ''
  return bps >= 1e9 ? String(bps / 1e9) + ' Gbps' : String(bps / 1e6) + ' Mbps'
}
const tristate = (v?: string): string => (v === 'true' ? 'yes' : v === 'false' ? 'no' : 'unknown')
const updateColors = { up_to_date: 'success', updates_available: 'warning', error: 'error', unsupported: 'neutral', unknown: 'neutral' } as const
const updateStatus = computed(() => inv.value?.update_state?.status || 'unknown')
const updateSummary = computed<KeyValue[]>(() => {
  const u = inv.value?.update_state ?? {}
  return kv([['Package manager', u.package_manager], ['Reboot required', tristate(u.reboot_required)], ['Automatic updates', tristate(u.automatic_updates)],
    ['Pending updates', u.pending_count ?? 0], ['Security updates', u.security_classified ? (u.security_count ?? 0) : 'not classified'], ['Checked', fmt(u.checked_at)]])
})
const bmcPortColumns: Column<Record<string, unknown>>[] = [{ key: 'channel', label: 'Channel', width: 'sm' }, { key: 'mac', label: 'MAC' }, { key: 'address', label: 'Address' }]
const guestColumns: Column<Record<string, unknown>>[] = [
  { key: 'guest_id', label: 'VMID', width: 'sm' }, { key: 'name', label: 'Name' }, { key: 'kind', label: 'Kind', width: 'sm' },
  { key: 'macs', label: 'MACs', format: (g) => ((g.macs as string[] | undefined) ?? []).join(', ') },
]
const guestRows = computed(() => rows((inv.value?.hypervisor_guests ?? []).map((g) => ({ ...g, guest_id: g.id }))))
const truncatedText = computed(() => {
  const t = inv.value?.truncated ?? {}
  const parts: [number | undefined, string][] = [[t.interfaces, 'interfaces'], [t.addresses, 'addresses'], [t.guests, 'guests'], [t.packages, 'pending updates'], [t.bmc_ports, 'BMC ports']]
  return parts.filter(([n]) => (n ?? 0) > 0).map(([n, what]) => String(n) + ' ' + what).join(', ')
})
const snapshotColumns: Column<Snapshot>[] = [
  { key: 'collected_at', label: 'Collected', format: (s) => fmt(s.collected_at) }, { key: 'received_at', label: 'Received', format: (s) => fmt(s.received_at), hideOnStack: true },
  { key: 'source', label: 'Source', width: 'sm' }, { key: 'agent_version', label: 'Agent', hideOnStack: true }, { key: 'short', label: 'ID', format: (s) => s.id.slice(0, 8) },
]
const changeColumns: Column<Row<Change>>[] = [
  { key: 'detected_at', label: 'Detected', format: (c) => fmt(c.detected_at) }, { key: 'category', label: 'Category' }, { key: 'change_type', label: 'Change', width: 'sm' }, { key: 'component_key', label: 'Component' },
]
const diffColumns: Column<Row<Change>>[] = [{ key: 'category', label: 'Category' }, { key: 'component_key', label: 'Component' }, { key: 'before', label: 'Before' }, { key: 'after', label: 'After' }]
const patchRows = computed(() => rows((inv.value?.patches ?? []).map((p) => ({ patch_id: p.id, installed_on: p.installed_on }))))

const snapshotOptions = computed(() => snapshots.items.map((s) => ({ title: fmt(s.collected_at) + ' · ' + s.id.slice(0, 8), value: s.id })))
async function compare(): Promise<void> {
  diffError.value = ''
  diff.value = null
  if (!diffA.value || !diffB.value) {
    diffError.value = 'Pick two snapshots to compare.'
    return
  }
  try {
    diff.value = await snapshots.diff(diffA.value, diffB.value)
  } catch (e) {
    diffError.value = describe(e)
  }
}
const grouped = (type: string) => rows((diff.value?.changes ?? []).filter((c) => c.change_type === type))
const changeColors = { added: 'success', removed: 'error', modified: 'warning' } as const
</script>

<template>
  <UiPage :title="host?.hostname ?? id">
    <template #before-title><UiButton variant="text" icon="mdi-arrow-left" icon-only label="Back to hosts" @click="router.push({ name: 'inventory-hosts' })" /></template>
    <template #badges>
      <UiStatusChip v-if="host" :status="host.status" />
      <UiStatusChip v-if="online" status="online" label="agent online" />
      <UiLiveIndicator :connected="live.connected" />
    </template>
    <template #actions>
      <UiButton size="sm" variant="soft" icon="mdi-refresh" :loading="busy" @click="refresh">Refresh</UiButton>
      <UiButton v-if="host && host.status !== 'retired'" size="sm" variant="soft" color="warning" :loading="busy" @click="retire">Retire</UiButton>
      <UiButton size="sm" variant="soft" color="error" :loading="busy" @click="remove">Delete</UiButton>
    </template>
    <UiAlert v-if="error" kind="error" class="mb-3">{{ error }}</UiAlert>
    <UiCard v-if="host" class="mb-4">
      <UiKeyValueTable :items="summary" :columns="2" />
      <div v-if="host.tags && Object.keys(host.tags).length" class="mt-3 flex flex-wrap gap-1"><UiBadge v-for="(v, k) in host.tags" :key="k">{{ k }}: {{ v }}</UiBadge></div>
    </UiCard>

    <UiTabs v-model="tab" :tabs="tabs" class="mb-4" />
    <UiAlert v-if="tab !== 'history' && !inv" kind="info">No snapshot collected yet.</UiAlert>

    <div v-if="tab === 'hardware' && inv" class="flex flex-col gap-4">
      <div class="grid grid-cols-1 gap-4 lg:grid-cols-2">
        <UiCard title="BIOS"><UiKeyValueTable :items="kv([['Vendor', inv.bios.vendor], ['Version', inv.bios.version], ['Release date', inv.bios.release_date]])" /></UiCard>
        <UiCard title="System"><UiKeyValueTable :items="kv([['Manufacturer', inv.system.manufacturer], ['Product', inv.system.product_name], ['Version', inv.system.version], ['Serial', inv.system.serial_number], ['Family', inv.system.family], ['SKU', inv.system.sku_number]])" /></UiCard>
        <UiCard title="Baseboard"><UiKeyValueTable :items="kv([['Manufacturer', inv.baseboard.manufacturer], ['Product', inv.baseboard.product], ['Version', inv.baseboard.version], ['Serial', inv.baseboard.serial_number], ['Asset tag', inv.baseboard.asset_tag]])" /></UiCard>
        <UiCard title="Chassis"><UiKeyValueTable :items="kv([['Manufacturer', inv.chassis.manufacturer], ['Type', inv.chassis.type], ['Version', inv.chassis.version], ['Serial', inv.chassis.serial_number], ['Asset tag', inv.chassis.asset_tag]])" /></UiCard>
      </div>
      <UiCard title="Processors" :padded="false"><UiDataTable :items="rows(inv.processors)" :columns="processorColumns" caption="Processors" empty-title="Not collected" /></UiCard>
      <UiCard title="Memory" :subtitle="'total ' + (humanBytes(inv.memory.total_physical_bytes) || '—')" :padded="false"><UiDataTable :items="rows(inv.memory.modules)" :columns="memoryColumns" caption="Memory modules" empty-title="Not collected" /></UiCard>
      <UiCard title="Disks">
        <UiEmptyState v-if="!(inv.disks ?? []).length" title="Not collected" />
        <div v-for="(d, i) in inv.disks ?? []" :key="i" class="mb-3">
          <div class="mb-1 flex flex-wrap items-center gap-1">
            <span class="font-medium">{{ d.model || 'Disk ' + (i + 1) }}</span>
            <UiBadge>{{ humanBytes(d.size_bytes) || '—' }}</UiBadge><UiBadge v-if="d.media_type">{{ d.media_type }}</UiBadge><UiBadge v-if="d.interface">{{ d.interface }}</UiBadge>
          </div>
          <UiDataTable :items="rows(d.partitions)" :columns="partitionColumns" caption="Partitions" empty-title="No partitions" />
        </div>
      </UiCard>
      <UiCard title="Monitors" :padded="false"><UiDataTable :items="rows(inv.monitors)" :columns="monitorColumns" caption="Monitors" empty-title="Not collected" /></UiCard>
    </div>

    <div v-if="tab === 'software' && inv" class="flex flex-col gap-4">
      <div class="grid grid-cols-1 gap-4 lg:grid-cols-2">
        <UiCard title="Operating system"><UiKeyValueTable :items="kv([['Name', inv.os.name], ['Version', inv.os.version], ['Build', inv.os.build], ['Arch', inv.os.arch], ['Kernel', inv.os.kernel], ['Install date', fmt(inv.os.install_date)], ['Last boot', fmt(inv.os.last_boot)]])" /></UiCard>
        <UiCard title="Environment"><UiKeyValueTable :items="kv([['Domain', inv.environment.domain], ['Workgroup', inv.environment.workgroup], ['Timezone', inv.environment.timezone], ['Locale', inv.environment.locale]])" /></UiCard>
      </div>
      <UiCard title="Updates" data-test="update-card">
        <div class="mb-2 flex flex-wrap items-center gap-1">
          <UiStatusChip :status="updateStatus" :colors="updateColors" :label="updateStatus.replace(/_/g, ' ')" />
          <UiBadge v-if="inv.update_state?.reboot_required === 'true'" color="warning">reboot required</UiBadge>
        </div>
        <UiKeyValueTable :items="updateSummary" :columns="2" />
      </UiCard>
      <UiCard title="Installed programs" :subtitle="String((inv.installed_programs ?? []).length)" :padded="false">
        <UiDataTable :items="rows(inv.installed_programs)" :columns="programColumns" caption="Installed programs" empty-title="Not collected" :virtual-at="200">
          <template #cell-available_version="{ row }">
            <span v-if="row.available_version">{{ row.available_version }} <UiBadge v-if="row.security_update" color="error" size="xs" data-test="security-update">security</UiBadge></span>
          </template>
        </UiDataTable>
      </UiCard>
      <UiCard title="Services" :subtitle="String((inv.services ?? []).length)" :padded="false">
        <UiDataTable :items="rows(inv.services)" :columns="serviceColumns" caption="Services" empty-title="Not collected" :virtual-at="200">
          <template #cell-state="{ row }"><UiStatusChip :status="String(row.state ?? '')" :colors="{ running: 'success', stopped: 'neutral' }" /></template>
        </UiDataTable>
      </UiCard>
      <div class="grid grid-cols-1 gap-4 lg:grid-cols-2">
        <UiCard title="Users" :padded="false"><UiDataTable :items="rows(inv.users)" :columns="userColumns" caption="Users" empty-title="Not collected" /></UiCard>
        <UiCard title="Patches" :padded="false"><UiDataTable :items="patchRows" :columns="patchColumns" caption="Patches" empty-title="Not collected" /></UiCard>
      </div>
    </div>

    <div v-if="tab === 'network' && inv" class="flex flex-col gap-4">
      <UiAlert v-if="truncatedText" kind="warning" data-test="truncated-notice">The agent dropped entries above its limits: {{ truncatedText }}.</UiAlert>
      <UiCard title="Addressing">
        <UiKeyValueTable :items="kv([['Primary IPv4', inv.primary_ipv4], ['Primary IPv6', inv.primary_ipv6], ['Virtualization', [inv.virtualization?.role, inv.virtualization?.kind].filter(Boolean).join(' · ')], ['OS family', inv.os.family]])" :columns="2" />
      </UiCard>
      <UiCard title="Network interfaces" :padded="false">
        <UiDataTable :items="rows(inv.network_interfaces)" :columns="nicColumns" caption="Network interfaces" empty-title="Not collected">
          <template #cell-addresses="{ row }">
            <div class="flex flex-col gap-0.5">
              <span v-for="(ad, i) in addressesOf(row)" :key="i" class="flex flex-wrap items-center gap-1">
                <span>{{ ad.text }}</span>
                <UiBadge v-if="ad.a?.dhcp" size="xs" soft data-test="addr-flag-dhcp">dhcp</UiBadge>
                <UiBadge v-if="ad.a?.temporary" size="xs" soft data-test="addr-flag-temporary">temporary</UiBadge>
                <UiBadge v-if="ad.a?.deprecated" size="xs" soft color="warning" data-test="addr-flag-deprecated">deprecated</UiBadge>
                <UiBadge v-if="ad.a && ad.a.scope && ad.a.scope !== 'global'" size="xs" soft>{{ ad.a.scope }}</UiBadge>
              </span>
            </div>
          </template>
          <template #cell-gateway="{ row }">
            <span class="flex flex-wrap items-center gap-1">{{ row.gateway }}<UiBadge v-if="row.default_route" size="xs" soft>default</UiBadge><UiBadge v-if="row.dhcp" size="xs" soft data-test="iface-dhcp">DHCP</UiBadge></span>
          </template>
          <template #cell-up="{ row }"><UiStatusChip :status="row.up ? 'up' : 'down'" :colors="{ up: 'success', down: 'neutral' }" /></template>
        </UiDataTable>
      </UiCard>
      <UiCard v-if="inv.bmc" title="BMC (out-of-band)" data-test="bmc-card">
        <UiKeyValueTable :items="kv([['Address', inv.bmc.address ? inv.bmc.address + (inv.bmc.prefix_length ? '/' + inv.bmc.prefix_length : '') : ''], ['Gateway', inv.bmc.gateway], ['IP source', inv.bmc.ip_source], ['VLAN', inv.bmc.vlan_id || '']])" :columns="2" />
        <UiDataTable :items="rows(inv.bmc.ports)" :columns="bmcPortColumns" caption="BMC ports" empty-title="No ports" class="mt-3" />
      </UiCard>
      <UiCard v-if="guestRows.length" title="Hypervisor guests" :subtitle="String(guestRows.length)" :padded="false" data-test="guests-card">
        <UiDataTable :items="guestRows" :columns="guestColumns" caption="Hypervisor guests" :virtual-at="200" />
      </UiCard>
    </div>

    <div v-if="tab === 'history'" class="flex flex-col gap-4">
      <UiCard title="Compare snapshots">
        <div class="grid grid-cols-1 gap-2 md:grid-cols-12 md:items-end">
          <div class="md:col-span-5"><UiSelect id="diff-from" v-model="diffA" label="From" :options="snapshotOptions" /></div>
          <div class="md:col-span-5"><UiSelect id="diff-to" v-model="diffB" label="To" :options="snapshotOptions" /></div>
          <div class="md:col-span-2"><UiButton block :disabled="!diffA || !diffB" data-test="diff-compare" @click="compare">Compare</UiButton></div>
        </div>
        <UiAlert v-if="diffError" kind="error" class="mt-3">{{ diffError }}</UiAlert>
        <div v-if="diff" class="mt-4 flex flex-col gap-3">
          <div v-for="type in (['added', 'removed', 'modified'] as const)" :key="type">
            <div class="mb-1 flex items-center gap-2"><UiStatusChip :status="type" :colors="changeColors" /><span class="text-sm text-base-content/70">{{ grouped(type).length }}</span></div>
            <UiDataTable v-if="grouped(type).length" :items="grouped(type)" :columns="diffColumns" :caption="type + ' changes'" />
          </div>
        </div>
      </UiCard>
      <UiCard title="Snapshots" :padded="false"><UiDataTable :items="snapshots.items" :columns="snapshotColumns" caption="Snapshots" empty-title="No snapshots" data-test="snapshots-table" /></UiCard>
      <UiCard title="Change history" :padded="false">
        <UiDataTable :items="rows(changes)" :columns="changeColumns" caption="Change history" empty-title="No changes recorded">
          <template #cell-change_type="{ row }"><UiStatusChip :status="String(row.change_type)" :colors="changeColors" /></template>
        </UiDataTable>
      </UiCard>
    </div>
  </UiPage>
</template>
