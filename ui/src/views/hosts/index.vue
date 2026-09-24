<script setup lang="ts">
import { computed, onMounted, onUnmounted } from 'vue'
import { useRouter } from 'vue-router'
import { UiPage, UiAlert, UiCard, UiForm, UiInput, UiSelect, UiButton, UiDataTable, UiStatusChip, UiLiveIndicator, type Column, type SelectOption } from '@go-tangra/ui'
import { useZodForm } from '@go-tangra/ui/forms'
import { useHosts } from '@/stores/hosts'
import { useAgents } from '@/stores/agents'
import { useLive } from '@/stores/live'
import { hostFilterSchema, HOST_STATUSES } from '@/schemas'
import type { Host } from '@/api/types'

const router = useRouter()
const store = useHosts()
const agents = useAgents()
const live = useLive()

const statusOptions: SelectOption[] = HOST_STATUSES.map((s) => ({ title: s, value: s }))
const lastSeenOptions: SelectOption[] = [{ title: 'Last 24 hours', value: '24h' }, { title: 'Last 7 days', value: '7d' }, { title: 'Last 30 days', value: '30d' }]

let release: (() => void) | null = null
onMounted(() => {
  void store.list()
  void agents.listConnected()
  release = live.connect()
})
onUnmounted(() => release?.())

function sinceIso(window?: string): string | undefined {
  if (!window) return undefined
  const ms = window === '24h' ? 864e5 : window === '7d' ? 7 * 864e5 : 30 * 864e5
  return new Date(Date.now() - ms).toISOString()
}
// The filter is a validated form too: a malformed tag never reaches the API.
const filter = useZodForm(hostFilterSchema, {
  initial: { hostname: '', os: '', manufacturer: '', tag: '' },
  onSubmit: (f) => store.list({ hostname: f.hostname || undefined, os: f.os || undefined, manufacturer: f.manufacturer || undefined, status: f.status, tag: f.tag || undefined, last_seen_from: sinceIso(f.last_seen) }),
})
const reload = () => void filter.submit()

const onlineHostIds = computed(() => new Set(agents.connected.map((a) => a.host_id).filter((h): h is string => !!h)))
const fmt = (ts?: string): string => (ts ? new Date(ts).toLocaleString() : '')
const columns: Column<Host>[] = [
  { key: 'hostname', label: 'Hostname', sortable: true },
  { key: 'os_name', label: 'OS', format: (h) => [h.os_name, h.os_version].filter(Boolean).join(' ') },
  { key: 'manufacturer', label: 'Manufacturer', hideOnStack: true },
  { key: 'model', label: 'Model', hideOnStack: true },
  { key: 'status', label: 'Status', width: 'sm' },
  { key: 'agent', label: 'Agent', width: 'sm', format: (h) => (onlineHostIds.value.has(h.id) ? 'online' : 'offline') },
  { key: 'last_seen', label: 'Last seen', format: (h) => fmt(h.last_seen), sortable: true },
]
function open(h: Host): void {
  void router.push({ name: 'inventory-host', params: { id: h.id } })
}
</script>

<template>
  <UiPage title="Hosts">
    <template #badges><UiLiveIndicator :connected="live.connected" /></template>
    <template #actions><UiButton variant="text" icon="mdi-refresh" icon-only label="Refresh" @click="reload" /></template>
    <template #filters>
      <UiForm :form="filter" class="w-full">
        <div class="grid grid-cols-2 gap-2 md:grid-cols-12 md:items-end">
          <div class="col-span-2 md:col-span-3"><UiInput v-bind="filter.field('hostname')" label="Hostname" size="sm" @enter="reload" /></div>
          <div class="md:col-span-2"><UiInput v-bind="filter.field('os')" label="OS" size="sm" @enter="reload" /></div>
          <div class="md:col-span-2"><UiInput v-bind="filter.field('manufacturer')" label="Manufacturer" size="sm" @enter="reload" /></div>
          <div class="md:col-span-2"><UiSelect v-bind="filter.field('status')" label="Status" :options="statusOptions" size="sm" @update:model-value="reload" /></div>
          <div class="md:col-span-2"><UiSelect v-bind="filter.field('last_seen')" label="Last seen" :options="lastSeenOptions" size="sm" @update:model-value="reload" /></div>
          <div class="md:col-span-1"><UiInput v-bind="filter.field('tag')" label="Tag" placeholder="k or k=v" size="sm" @enter="reload" /></div>
        </div>
      </UiForm>
    </template>
    <UiAlert v-if="store.error" kind="error" class="mb-3">{{ store.error }}</UiAlert>
    <UiCard :padded="false">
      <UiDataTable :items="store.items" :columns="columns" :loading="store.loading" caption="Hosts" empty-title="No hosts match" clickable :row-attrs="(h) => ({ 'data-test': 'host-row-' + h.id })" data-test="hosts-table" @row-click="open">
        <template #cell-status="{ row }"><UiStatusChip :status="row.status" /></template>
        <template #cell-agent="{ row }"><UiStatusChip :status="onlineHostIds.has(row.id) ? 'online' : 'offline'" :colors="{ offline: 'neutral' }" :data-test="onlineHostIds.has(row.id) ? 'host-online-' + row.id : undefined" /></template>
      </UiDataTable>
    </UiCard>
  </UiPage>
</template>
