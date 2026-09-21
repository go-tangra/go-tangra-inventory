<script setup lang="ts">
import { computed, onMounted, onUnmounted, ref } from 'vue'
import { useRouter } from 'vue-router'
import { useHosts } from '@/stores/hosts'
import { useAgents } from '@/stores/agents'
import { useLive } from '@/stores/live'
import type { HostFilter } from '@/stores/hosts'

const router = useRouter()
const store = useHosts()
const agents = useAgents()
const live = useLive()

const hostname = ref('')
const os = ref('')
const manufacturer = ref('')
const status = ref<string | null>(null)
const tag = ref('')
const lastSeen = ref<string | null>(null)

const STATUSES = ['active', 'stale', 'retired']
const LAST_SEEN = [
  { title: 'Last 24 hours', value: '24h' },
  { title: 'Last 7 days', value: '7d' },
  { title: 'Last 30 days', value: '30d' },
]

let release: (() => void) | null = null

onMounted(() => {
  void store.list()
  void agents.listConnected()
  release = live.connect()
})
onUnmounted(() => release?.())

function sinceIso(window: string | null): string | undefined {
  if (!window) return undefined
  const now = Date.now()
  const ms = window === '24h' ? 864e5 : window === '7d' ? 7 * 864e5 : 30 * 864e5
  return new Date(now - ms).toISOString()
}

function reload(): void {
  const filter: HostFilter = {
    hostname: hostname.value.trim() || undefined,
    os: os.value.trim() || undefined,
    manufacturer: manufacturer.value.trim() || undefined,
    status: status.value ?? undefined,
    tag: tag.value.trim() || undefined,
    last_seen_from: sinceIso(lastSeen.value),
  }
  void store.list(filter)
}

const onlineHostIds = computed(() => new Set(agents.connected.map((a) => a.host_id).filter((h): h is string => !!h)))

const statusColor: Record<string, string> = {
  active: 'success',
  stale: 'warning',
  retired: 'grey',
}

function open(id: string): void {
  void router.push({ name: 'inventory-host', params: { id } })
}

function fmt(ts?: string): string {
  return ts ? new Date(ts).toLocaleString() : '—'
}
</script>

<template>
  <div>
    <div class="d-flex align-center mb-4">
      <h1 class="text-h5">Hosts</h1>
      <v-chip v-if="live.connected" size="x-small" color="success" variant="tonal" class="ms-3">live</v-chip>
      <v-spacer />
      <v-btn variant="text" icon="mdi-refresh" @click="reload" />
    </div>

    <v-card variant="tonal" class="mb-4">
      <v-card-text>
        <v-row dense>
          <v-col cols="12" sm="6" md="3">
            <v-text-field v-model="hostname" label="Hostname" density="compact" clearable hide-details @keyup.enter="reload" @click:clear="reload" />
          </v-col>
          <v-col cols="12" sm="6" md="2">
            <v-text-field v-model="os" label="OS" density="compact" clearable hide-details @keyup.enter="reload" @click:clear="reload" />
          </v-col>
          <v-col cols="12" sm="6" md="2">
            <v-text-field v-model="manufacturer" label="Manufacturer" density="compact" clearable hide-details @keyup.enter="reload" @click:clear="reload" />
          </v-col>
          <v-col cols="12" sm="6" md="2">
            <v-select v-model="status" :items="STATUSES" label="Status" density="compact" clearable hide-details @update:model-value="reload" />
          </v-col>
          <v-col cols="12" sm="6" md="2">
            <v-select v-model="lastSeen" :items="LAST_SEEN" label="Last seen" density="compact" clearable hide-details @update:model-value="reload" />
          </v-col>
          <v-col cols="12" sm="6" md="1">
            <v-text-field v-model="tag" label="Tag" density="compact" clearable hide-details placeholder="k or k=v" @keyup.enter="reload" @click:clear="reload" />
          </v-col>
        </v-row>
      </v-card-text>
    </v-card>

    <v-alert v-if="store.error" type="error" variant="tonal" density="compact" class="mb-3">{{ store.error }}</v-alert>

    <v-table data-test="hosts-table">
      <thead>
        <tr><th>Hostname</th><th>OS</th><th>Manufacturer</th><th>Model</th><th>Status</th><th>Agent</th><th>Last seen</th></tr>
      </thead>
      <tbody>
        <tr v-for="h in store.items" :key="h.id" class="cursor-pointer" :data-test="'host-row-' + h.id" @click="open(h.id)">
          <td>{{ h.hostname }}</td>
          <td class="text-medium-emphasis">{{ h.os_name }} {{ h.os_version }}</td>
          <td class="text-medium-emphasis">{{ h.manufacturer || '—' }}</td>
          <td class="text-medium-emphasis">{{ h.model || '—' }}</td>
          <td><v-chip size="x-small" :color="statusColor[h.status]" variant="flat">{{ h.status }}</v-chip></td>
          <td>
            <v-chip v-if="onlineHostIds.has(h.id)" size="x-small" color="success" variant="tonal" prepend-icon="mdi-circle" :data-test="'host-online-' + h.id">online</v-chip>
            <v-chip v-else size="x-small" color="grey" variant="tonal">offline</v-chip>
          </td>
          <td class="text-medium-emphasis">{{ fmt(h.last_seen) }}</td>
        </tr>
        <tr v-if="!store.items.length && !store.loading">
          <td colspan="7" class="text-medium-emphasis">No hosts match.</td>
        </tr>
      </tbody>
    </v-table>
  </div>
</template>

<style scoped>
.cursor-pointer { cursor: pointer; }
</style>
