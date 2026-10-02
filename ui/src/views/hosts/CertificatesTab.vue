<script setup lang="ts">
// Certificates delivered to this host (feature 033): one row per name with
// the latest state, and the paged delivery history. Identity only (serial,
// fingerprint, common name, expiry) — the API never returns material.
import { computed, onMounted, onUnmounted, ref, watch } from 'vue'
import { useAbility } from '@casl/vue'
import { UiAlert, UiBadge, UiButton, UiCard, UiCopyButton, UiDataTable, UiSelect, UiStatusChip, useConfirm, useListQuery, type Column } from '@go-tangra/ui'
import { ACTIVE_STATES, DELIVERY_LIST, HOST_CERT_LIST, useCertificates } from '@/stores/certificates'
import { useLive } from '@/stores/live'
import { describe } from '@/api/client'
import type { CertificateDeliveryItem, HostCertificate } from '@/api/types'
import { RELOAD_DEBOUNCE_MS, STATE_COLORS, deployerLink, hookText, reasonText, shortFingerprint, stateLabel, triggerLabel } from './cert-text'

const props = defineProps<{ hostId: string }>()

const certificates = useCertificates()
const live = useLive()
const confirm = useConfirm()
const ability = useAbility()
// Cancelling a queued delivery follows agents:manage.
const canManage = computed(() => ability.can('manage', 'InventoryAgent'))

const rows = ref<HostCertificate[]>([])
const rowsTotal = ref(0)
const rowsLoading = ref(false)
const history = ref<CertificateDeliveryItem[]>([])
const historyTotal = ref(0)
const historyLoading = ref(false)
const error = ref('')
const message = ref('')
const busy = ref<string | null>(null)
const revokedFilter = ref('')
const revokedOptions = [{ title: 'All certificates', value: '' }, { title: 'Revoked only', value: 'true' }, { title: 'Not revoked', value: 'false' }]

// --- server paging and sorting (?certificates.page=…, ?deliveries.page=…) ---
const certLq = useListQuery('certificates', HOST_CERT_LIST)
const deliveryLq = useListQuery('deliveries', DELIVERY_LIST)

let certSeq = 0
async function loadCertificates(): Promise<void> {
  const mine = ++certSeq
  rowsLoading.value = true
  try {
    const f = revokedFilter.value ? { revoked: revokedFilter.value === 'true' } : {}
    const res = await certificates.forHost(props.hostId, certLq.query.value, f)
    if (mine !== certSeq) return
    rows.value = res.items
    rowsTotal.value = res.total
    if (res.page) certLq.clampTo(res.page) // a page beyond the end answers the last page
  } catch (e) {
    if (mine === certSeq) error.value = describe(e)
  } finally {
    if (mine === certSeq) rowsLoading.value = false
  }
}
let historySeq = 0
async function loadHistory(): Promise<void> {
  const mine = ++historySeq
  historyLoading.value = true
  try {
    const res = await certificates.deliveries(deliveryLq.query.value, { host_id: props.hostId })
    if (mine !== historySeq) return
    history.value = res.items
    historyTotal.value = res.total
    if (res.page) deliveryLq.clampTo(res.page)
  } catch (e) {
    if (mine === historySeq) error.value = describe(e)
  } finally {
    if (mine === historySeq) historyLoading.value = false
  }
}
function reload(): void {
  void loadCertificates()
  void loadHistory()
}
watch(certLq.query, () => void loadCertificates())
watch(deliveryLq.query, () => void loadHistory())
/** A new filter returns to page 1 (which reloads). */
function onRevokedFilter(): void {
  if (certLq.page.value !== 1) certLq.resetPage()
  else void loadCertificates()
}

// Delivery events are content-free (ids and state): reload this host's
// tables once a burst settles.
let timer: ReturnType<typeof setTimeout> | null = null
function scheduleReload(): void {
  if (timer) clearTimeout(timer)
  timer = setTimeout(() => {
    timer = null
    reload()
  }, RELOAD_DEBOUNCE_MS)
}
let release: (() => void) | null = null
let off: (() => void) | null = null
onMounted(() => {
  reload()
  release = live.connect()
  off = live.on((type, data) => {
    if (type.endsWith('certificate.delivery') && (data as { host_id?: string } | null)?.host_id === props.hostId) scheduleReload()
  })
})
onUnmounted(() => {
  if (timer) clearTimeout(timer)
  off?.()
  release?.()
})

async function cancel(item: CertificateDeliveryItem): Promise<void> {
  if (!(await confirm.ask({ title: `Cancel the delivery of ${item.name}?`, text: 'The agent will not install this certificate. Files already on the host stay.', danger: true, confirmLabel: 'Cancel delivery', cancelLabel: 'Keep' }))) return
  busy.value = item.id
  error.value = ''
  message.value = ''
  try {
    await certificates.cancel(item.id)
    message.value = `Delivery of ${item.name} cancelled.`
    reload()
  } catch (e) {
    error.value = describe(e)
  } finally {
    busy.value = null
  }
}
const cancellable = (i: CertificateDeliveryItem | null | undefined): i is CertificateDeliveryItem => !!i && ACTIVE_STATES.has(i.state)

const fmt = (ts?: string | null): string => (ts ? new Date(ts).toLocaleString() : '')
type CertRow = HostCertificate & Record<string, unknown>
type ItemRow = CertificateDeliveryItem & Record<string, unknown>
const certRows = computed(() => rows.value as CertRow[])
const historyRows = computed(() => history.value as ItemRow[])
// Sortable columns are the server's sort fields.
const certColumns: Column<CertRow>[] = [
  { key: 'name', label: 'Name', sortable: true },
  { key: 'common_name', label: 'Common name' },
  { key: 'serial', label: 'Serial', hideOnStack: true },
  { key: 'fingerprint_sha256', label: 'Fingerprint (SHA-256)', hideOnStack: true },
  { key: 'not_after', label: 'Expires', sortable: true, format: (r) => fmt(r.not_after) },
  { key: 'state', label: 'State', sortable: true },
  { key: 'hook_exit_code', label: 'Hook', hideOnStack: true },
  { key: 'last_delivered_at', label: 'Last delivered', sortable: true, defaultDir: 'desc', hideOnStack: true, format: (r) => fmt(r.last_delivered_at) },
]
const historyColumns: Column<ItemRow>[] = [
  { key: 'created_at', label: 'Requested', sortable: true, defaultDir: 'desc', format: (i) => fmt(i.created_at) },
  { key: 'name', label: 'Name', sortable: true },
  { key: 'state', label: 'State', sortable: true },
  { key: 'trigger', label: 'Trigger', hideOnStack: true, format: (i) => triggerLabel(i.trigger) },
  { key: 'attempts', label: 'Attempts', align: 'end', hideOnStack: true },
  { key: 'fingerprint_sha256', label: 'Fingerprint', hideOnStack: true, format: (i) => shortFingerprint(i.fingerprint_sha256) },
  { key: 'hook_exit_code', label: 'Hook', hideOnStack: true, format: (i) => hookText(i.hook_exit_code) },
  { key: 'updated_at', label: 'Updated', sortable: true, defaultDir: 'desc', hideOnStack: true, format: (i) => fmt(i.updated_at) },
]
</script>

<template>
  <div class="flex flex-col gap-4">
    <UiAlert v-if="message" kind="success" data-test="cert-message">{{ message }}</UiAlert>
    <UiAlert v-if="error" kind="error">{{ error }}</UiAlert>
    <UiCard title="Certificates" :padded="false">
      <div class="max-w-xs p-3">
        <UiSelect id="cert-revoked" v-model="revokedFilter" label="Revocation" size="sm" :options="revokedOptions" data-test="cert-revoked-filter" @update:model-value="onRevokedFilter" />
      </div>
      <UiDataTable :items="certRows" :columns="certColumns" row-key="name" :loading="rowsLoading" :total="rowsTotal" :page="certLq.page.value" :page-size="certLq.pageSize.value" :sort="certLq.sort.value" caption="Certificates on this host" empty-title="No certificates delivered" :row-attrs="(r) => ({ 'data-test': 'cert-row-' + r.name })" data-test="certificates-table" @update:page="certLq.setPage" @update:page-size="certLq.setPageSize" @update:sort="certLq.setSort">
        <template #cell-name="{ row }">
          <span class="flex flex-wrap items-center gap-1">
            <span class="font-medium">{{ row.name }}</span>
            <UiBadge v-if="row.revoked" color="error" size="xs" :data-test="'cert-revoked-' + row.name">revoked</UiBadge>
          </span>
        </template>
        <template #cell-fingerprint_sha256="{ row }">
          <span v-if="row.fingerprint_sha256" class="inline-flex items-center gap-1 font-mono text-xs">
            <span :data-test="'cert-fp-' + row.name">{{ shortFingerprint(row.fingerprint_sha256) }}</span>
            <UiCopyButton :value="row.fingerprint_sha256" label="Copy fingerprint" size="xs" />
          </span>
        </template>
        <template #cell-state="{ row }">
          <span class="inline-flex flex-col gap-0.5">
            <UiStatusChip :status="row.state" :label="stateLabel(row.state)" :colors="STATE_COLORS" />
            <span v-if="row.reason" class="text-xs text-base-content/70" :data-test="'cert-reason-' + row.name">{{ reasonText(row.reason) }}</span>
            <span v-if="row.active_item && row.active_item.state !== row.state" class="inline-flex items-center gap-1 text-xs text-base-content/70" :data-test="'cert-queued-' + row.name">
              next: <UiStatusChip :status="row.active_item.state" :label="stateLabel(row.active_item.state)" :colors="STATE_COLORS" />
            </span>
          </span>
        </template>
        <template #cell-hook_exit_code="{ row }"><span :data-test="'cert-hook-' + row.name">{{ hookText(row.hook_exit_code) }}</span></template>
        <template #actions="{ row }">
          <a v-if="row.configuration_id" class="link link-hover text-sm" :href="deployerLink(row.configuration_id)" :data-test="'cert-deployer-' + row.name">Open in deployer</a>
          <UiButton v-if="canManage && cancellable(row.active_item)" size="xs" variant="text" color="error" :loading="busy === row.active_item.id" :data-test="'cert-cancel-' + row.active_item.id" @click="cancel(row.active_item)">Cancel delivery</UiButton>
        </template>
      </UiDataTable>
    </UiCard>
    <UiCard title="Delivery history" :padded="false">
      <UiDataTable :items="historyRows" :columns="historyColumns" :loading="historyLoading" :total="historyTotal" :page="deliveryLq.page.value" :page-size="deliveryLq.pageSize.value" :sort="deliveryLq.sort.value" caption="Certificate delivery history" empty-title="No deliveries" :row-attrs="(i) => ({ 'data-test': 'delivery-row-' + i.id })" data-test="deliveries-table" @update:page="deliveryLq.setPage" @update:page-size="deliveryLq.setPageSize" @update:sort="deliveryLq.setSort">
        <template #cell-state="{ row }">
          <span class="inline-flex flex-col gap-0.5">
            <UiStatusChip :status="row.state" :label="stateLabel(row.state)" :colors="STATE_COLORS" />
            <span v-if="row.reason" class="text-xs text-base-content/70" :data-test="'delivery-reason-' + row.id">{{ reasonText(row.reason) }}</span>
          </span>
        </template>
        <template #actions="{ row }">
          <UiButton v-if="canManage && cancellable(row)" size="xs" variant="text" color="error" :loading="busy === row.id" :data-test="'delivery-cancel-' + row.id" @click="cancel(row)">Cancel</UiButton>
        </template>
      </UiDataTable>
    </UiCard>
  </div>
</template>
