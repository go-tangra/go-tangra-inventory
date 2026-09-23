<script setup lang="ts">
import { computed, onMounted } from 'vue'
import { UiPage, UiCard, UiStatGrid, UiStatTile, UiBarList, type BarItem } from '@freya/ui'
import { useHosts } from '@/stores/hosts'
import { useAgents } from '@/stores/agents'
import { useStats } from '@/stores/stats'

// The dashboard prefers the /statistics/tenant snapshot; when it is unavailable
// it derives figures from the loaded host list and connected-agent registry.
const hosts = useHosts()
const agents = useAgents()
const stats = useStats()

onMounted(() => {
  void hosts.list()
  void agents.listConnected()
  void stats.load()
})

const snap = computed(() => stats.snapshot)
function humanBytes(bytes: number): string {
  if (!bytes) return '0 B'
  const units = ['B', 'KB', 'MB', 'GB', 'TB', 'PB']
  const i = Math.floor(Math.log(bytes) / Math.log(1024))
  return (bytes / Math.pow(1024, i)).toFixed(i ? 1 : 0) + ' ' + units[i]
}
function tally(pick: (h: (typeof hosts.items)[number]) => string | undefined): Record<string, number> {
  const m: Record<string, number> = {}
  for (const h of hosts.items) {
    const k = pick(h) || 'unknown'
    m[k] = (m[k] ?? 0) + 1
  }
  return m
}
const bars = (m: Record<string, number>, color?: (k: string) => NonNullable<BarItem['color']>): BarItem[] =>
  Object.entries(m).sort((a, b) => b[1] - a[1]).map(([label, value]) => (color ? { label, value, color: color(label) } : { label, value }))

const hostsTotal = computed(() => snap.value?.hosts_total ?? hosts.items.length)
const agentsOnline = computed(() => snap.value?.agents_online ?? agents.connected.length)
const agentsOffline = computed(() => snap.value?.agents_offline ?? Math.max(0, hostsTotal.value - agentsOnline.value))
const staleHosts = computed(() => snap.value?.stale_hosts ?? hosts.items.filter((h) => h.status === 'stale').length)
const snapshotsTotal = computed(() => snap.value?.snapshots_total ?? 0)
const statusColor: Record<string, BarItem['color']> = { active: 'success', stale: 'warning', retired: 'neutral' }
const byStatus = computed(() => bars(snap.value?.hosts_by_status ?? tally((h) => h.status), (k) => statusColor[k] ?? 'primary'))
const byOs = computed(() => bars(snap.value?.hosts_by_os ?? tally((h) => h.os_name)))
const byManufacturer = computed(() => bars(snap.value?.hosts_by_manufacturer ?? tally((h) => h.manufacturer), () => 'info'))
</script>

<template>
  <UiPage title="Inventory">
    <UiStatGrid class="mb-3" :cols="4">
      <UiStatTile title="Hosts" :value="hostsTotal" icon="mdi-desktop-classic" color="primary" />
      <UiStatTile title="Agents online" :value="agentsOnline" icon="mdi-lan-connect" color="success" :subtitle="agentsOffline + ' offline'" />
      <UiStatTile title="Stale hosts" :value="staleHosts" icon="mdi-clock-alert-outline" color="warning" />
      <UiStatTile title="Snapshots" :value="snapshotsTotal" icon="mdi-camera-outline" color="info" />
    </UiStatGrid>
    <UiStatGrid class="mb-4" :cols="3">
      <UiStatTile title="Total RAM" :value="humanBytes(snap?.total_ram_bytes ?? 0)" icon="mdi-memory" color="secondary" />
      <UiStatTile title="CPU cores" :value="snap?.total_cpu_cores ?? 0" icon="mdi-cpu-64-bit" color="accent" />
      <UiStatTile title="Total disk" :value="humanBytes(snap?.total_disk_bytes ?? 0)" icon="mdi-harddisk" color="neutral" />
    </UiStatGrid>
    <div class="grid grid-cols-1 gap-4 lg:grid-cols-3">
      <UiCard title="Hosts by status"><UiBarList :items="byStatus" empty-title="No hosts yet" /></UiCard>
      <UiCard title="Hosts by OS"><UiBarList :items="byOs" empty-title="No hosts yet" /></UiCard>
      <UiCard title="Hosts by manufacturer"><UiBarList :items="byManufacturer" empty-title="No hosts yet" /></UiCard>
    </div>
  </UiPage>
</template>
