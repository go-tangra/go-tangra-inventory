<script setup lang="ts">
import { computed, onMounted } from 'vue'
import { useHosts } from '@/stores/hosts'
import { useAgents } from '@/stores/agents'
import { useStats } from '@/stores/stats'
import StatsCard from '@/components/StatsCard.vue'

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

const hostsTotal = computed(() => snap.value?.hosts_total ?? hosts.items.length)
const agentsOnline = computed(() => snap.value?.agents_online ?? agents.connected.length)
const agentsOffline = computed(() => snap.value?.agents_offline ?? Math.max(0, hostsTotal.value - agentsOnline.value))
const staleHosts = computed(() => snap.value?.stale_hosts ?? hosts.items.filter((h) => h.status === 'stale').length)
const snapshotsTotal = computed(() => snap.value?.snapshots_total ?? 0)

const totalRam = computed(() => humanBytes(snap.value?.total_ram_bytes ?? 0))
const totalCores = computed(() => snap.value?.total_cpu_cores ?? 0)
const totalDisk = computed(() => humanBytes(snap.value?.total_disk_bytes ?? 0))

const byStatus = computed<Record<string, number>>(() => snap.value?.hosts_by_status ?? tally((h) => h.status))
const byOs = computed<[string, number][]>(() => Object.entries(snap.value?.hosts_by_os ?? tally((h) => h.os_name)).sort((a, b) => b[1] - a[1]))
const byManufacturer = computed<[string, number][]>(() => Object.entries(snap.value?.hosts_by_manufacturer ?? tally((h) => h.manufacturer)).sort((a, b) => b[1] - a[1]))

const statusMax = computed(() => Math.max(1, ...Object.values(byStatus.value)))
const osMax = computed(() => Math.max(1, ...byOs.value.map(([, n]) => n)))
const mfrMax = computed(() => Math.max(1, ...byManufacturer.value.map(([, n]) => n)))

const statusColor: Record<string, string> = { active: 'success', stale: 'warning', retired: 'grey' }
</script>

<template>
  <div>
    <h1 class="text-h5 mb-4">Inventory</h1>
    <v-row>
      <v-col cols="12" sm="6" md="3"><StatsCard title="Hosts" :value="hostsTotal" icon="mdi-desktop-classic" color="primary" /></v-col>
      <v-col cols="12" sm="6" md="3"><StatsCard title="Agents online" :value="agentsOnline" icon="mdi-lan-connect" color="success" :subtitle="agentsOffline + ' offline'" /></v-col>
      <v-col cols="12" sm="6" md="3"><StatsCard title="Stale hosts" :value="staleHosts" icon="mdi-clock-alert-outline" color="warning" /></v-col>
      <v-col cols="12" sm="6" md="3"><StatsCard title="Snapshots" :value="snapshotsTotal" icon="mdi-camera-outline" color="info" /></v-col>
    </v-row>

    <v-row class="mt-2">
      <v-col cols="12" sm="6" md="4"><StatsCard title="Total RAM" :value="totalRam" icon="mdi-memory" color="teal" /></v-col>
      <v-col cols="12" sm="6" md="4"><StatsCard title="CPU cores" :value="totalCores" icon="mdi-cpu-64-bit" color="deep-purple" /></v-col>
      <v-col cols="12" sm="6" md="4"><StatsCard title="Total disk" :value="totalDisk" icon="mdi-harddisk" color="blue-grey" /></v-col>
    </v-row>

    <v-row class="mt-2">
      <v-col cols="12" md="4">
        <v-card>
          <v-card-title class="text-subtitle-1">Hosts by status</v-card-title>
          <v-card-text>
            <div v-for="(n, s) in byStatus" :key="s" class="d-flex align-center mb-2">
              <v-chip size="x-small" :color="statusColor[s]" variant="flat" class="me-3" style="min-width: 84px; justify-content: center">{{ s }}</v-chip>
              <v-progress-linear :model-value="(n / statusMax) * 100" height="8" rounded :color="statusColor[s]" />
              <span class="ms-3 text-body-2">{{ n }}</span>
            </div>
            <div v-if="!Object.keys(byStatus).length" class="text-medium-emphasis">No hosts yet.</div>
          </v-card-text>
        </v-card>
      </v-col>
      <v-col cols="12" md="4">
        <v-card>
          <v-card-title class="text-subtitle-1">Hosts by OS</v-card-title>
          <v-card-text>
            <div v-for="[os, n] in byOs" :key="os" class="d-flex align-center mb-2">
              <v-chip size="x-small" variant="tonal" class="me-3" style="min-width: 110px; justify-content: center">{{ os }}</v-chip>
              <v-progress-linear :model-value="(n / osMax) * 100" height="8" rounded color="primary" />
              <span class="ms-3 text-body-2">{{ n }}</span>
            </div>
            <div v-if="!byOs.length" class="text-medium-emphasis">No hosts yet.</div>
          </v-card-text>
        </v-card>
      </v-col>
      <v-col cols="12" md="4">
        <v-card>
          <v-card-title class="text-subtitle-1">Hosts by manufacturer</v-card-title>
          <v-card-text>
            <div v-for="[mfr, n] in byManufacturer" :key="mfr" class="d-flex align-center mb-2">
              <v-chip size="x-small" variant="tonal" class="me-3" style="min-width: 110px; justify-content: center">{{ mfr }}</v-chip>
              <v-progress-linear :model-value="(n / mfrMax) * 100" height="8" rounded color="info" />
              <span class="ms-3 text-body-2">{{ n }}</span>
            </div>
            <div v-if="!byManufacturer.length" class="text-medium-emphasis">No hosts yet.</div>
          </v-card-text>
        </v-card>
      </v-col>
    </v-row>
  </div>
</template>
