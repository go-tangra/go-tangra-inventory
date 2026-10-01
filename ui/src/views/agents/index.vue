<script setup lang="ts">
import { computed, onMounted, onUnmounted, ref, watch } from 'vue'
import { useAbility } from '@casl/vue'
import { UiPage, UiAlert, UiCard, UiButton, UiDataTable, UiStatusChip, UiLiveIndicator, UiForm, UiInput, UiSecretField, UiCopyButton, UiDrawer, UiSelect, useConfirm, useListQuery, type Column } from '@go-tangra/ui'
import { useZodForm } from '@go-tangra/ui/forms'
import { FLEET_LIST, useAgents, type FleetFilter } from '@/stores/agents'
import { useLive } from '@/stores/live'
import { describe } from '@/api/client'
import { enrollTokenSchema } from '@/schemas'
import type { AgentFleetEntry, FleetState, MintedToken, UpgradeBatchResult } from '@/api/types'
import { fleetStateLabel, reasonText, skipSummary, UPGRADABLE } from './upgrade-text'
import PolicyCard from './PolicyCard.vue'
import AutoEnrollCard from './AutoEnrollCard.vue'

const agents = useAgents()
const live = useLive()
const confirm = useConfirm()
const ability = useAbility()
// Enrolment, refresh, revoke and routine upgrades all follow agents:manage.
const canManage = computed(() => ability.can('manage', 'InventoryAgent'))

const enrollOpen = ref(false)
const minted = ref<MintedToken | null>(null)
const message = ref('')
const error = ref('')
const busyHost = ref<string | null>(null)
const busyUpgrade = ref(false)
const selected = ref<string[]>([])
const stateFilter = ref<FleetState | ''>('')

// --- server paging and sorting (page / size / sort in the URL: ?agents.page=…) ---
const lq = useListQuery('agents', FLEET_LIST)
// Fleet-wide figures (the table shows one page): agents that need a manual
// install, and outdated agents (gates "Upgrade all outdated").
const manualCount = ref(0)
const outdatedCount = ref(0)

const fleetFilter = (): FleetFilter => (stateFilter.value ? { upgrade_state: stateFilter.value } : {})
async function load(): Promise<void> {
  const res = await agents.listFleet(fleetFilter(), lq.query.value)
  if (res?.page) lq.clampTo(res.page) // a page beyond the end answers the last page
}
async function counts(): Promise<void> {
  const [manual, outdated] = await Promise.all([agents.countFleet({ upgrade_state: 'manual_upgrade_required' }), agents.countFleet({ outdated: true })])
  manualCount.value = manual
  outdatedCount.value = outdated
}
/** Reloads the current page and the fleet-wide figures. */
async function reload(): Promise<void> {
  await Promise.all([load(), counts()])
}
watch(lq.query, () => void load())
/** A new state filter returns to page 1 (which reloads). */
function onStateFilter(): void {
  if (lq.page.value !== 1) lq.resetPage()
  else void load()
}

let release: (() => void) | null = null
let off: (() => void) | null = null
onMounted(() => {
  void reload()
  release = live.connect()
  // Upgrade events are content-free: refetch the fleet on each one.
  off = live.on((type) => {
    if (type.endsWith('agent.upgrade')) void reload()
  })
})
onUnmounted(() => {
  off?.()
  release?.()
})

// The secret is returned once; it lives only in this component's state while the dialog is open.
const enrollForm = useZodForm(enrollTokenSchema, {
  initial: { label: '' },
  onSubmit: async (v) => {
    minted.value = await agents.mintEnrollToken(v.label)
  },
})
function openEnroll(): void {
  minted.value = null
  enrollForm.reset({ label: '' })
  enrollOpen.value = true
}
function closeEnroll(): void {
  enrollOpen.value = false
  minted.value = null
}

async function refresh(hostId?: string): Promise<void> {
  if (!hostId) return
  busyHost.value = hostId
  message.value = ''
  error.value = ''
  try {
    const res = await agents.refresh(hostId)
    message.value = res.delivered ? 'Refresh delivered to the agent.' : 'Refresh queued; the agent is not currently connected.'
  } catch (e) {
    error.value = describe(e)
  } finally {
    busyHost.value = null
  }
}
async function revoke(a: AgentFleetEntry): Promise<void> {
  if (!(await confirm.ask({ title: 'Revoke this agent?', text: 'It must enrol again with a new token.', danger: true, confirmLabel: 'Revoke' }))) return
  error.value = ''
  try {
    await agents.revoke(a.agent_id)
  } catch (e) {
    error.value = describe(e)
  }
}

async function runUpgrade(action: () => Promise<UpgradeBatchResult>): Promise<void> {
  busyUpgrade.value = true
  message.value = ''
  error.value = ''
  try {
    const res = await action()
    message.value = `${res.created.length} upgrade${res.created.length === 1 ? '' : 's'} to ${res.target_version} requested.` + skipSummary(res.skipped)
    selected.value = []
    await reload()
  } catch (e) {
    error.value = describe(e)
  } finally {
    busyUpgrade.value = false
  }
}
const upgradeOne = (a: AgentFleetEntry): Promise<void> => runUpgrade(() => agents.upgrade([a.agent_id]))
const upgradeSelected = (): Promise<void> => runUpgrade(() => agents.upgrade(selected.value))
async function upgradeAll(): Promise<void> {
  if (!(await confirm.ask({ title: 'Upgrade all outdated agents?', text: `Every outdated agent that supports self-upgrade is asked to install ${agents.currentVersion || 'the current version'}.`, confirmLabel: 'Upgrade all' }))) return
  await runUpgrade(() => agents.upgradeAllOutdated())
}
async function cancel(a: AgentFleetEntry): Promise<void> {
  if (!a.upgrade_id) return
  error.value = ''
  message.value = ''
  try {
    await agents.cancelUpgrade(a.upgrade_id)
    message.value = 'Upgrade cancelled.'
    await reload()
  } catch (e) {
    error.value = describe(e)
  }
}

const canUpgrade = (a: AgentFleetEntry): boolean => !!a.upgrade_state && UPGRADABLE.has(a.upgrade_state)

const stateColors = { up_to_date: 'success', available: 'info', pending: 'neutral', in_progress: 'primary', failed: 'error', rolled_back: 'warning', manual_upgrade_required: 'warning', unsupported: 'neutral' } as const
const stateOptions = [{ title: 'All states', value: '' }, ...(Object.keys(stateColors) as FleetState[]).map((s) => ({ title: fleetStateLabel(s), value: s }))]

const fmt = (ts?: string): string => (ts ? new Date(ts).toLocaleString() : '')
// Sortable columns are the server's sort fields (FLEET_LIST; "state" is the
// upgrade state): sorting orders the whole fleet, not the visible page.
const columns: Column<AgentFleetEntry>[] = [
  { key: 'status', label: 'Status', width: 'sm' },
  { key: 'hostname', label: 'Hostname', sortable: true },
  { key: 'agent_id', label: 'Agent ID', format: (a) => a.agent_id.slice(0, 12), hideOnStack: true },
  { key: 'version', label: 'Version', sortable: true },
  { key: 'target_version', label: 'Target', hideOnStack: true },
  { key: 'state', label: 'Upgrade', sortable: true },
  { key: 'enrolled_via', label: 'Enrolled with', hideOnStack: true, format: (a) => (a.enrolled_via === 'auto' ? 'key ' + (a.auto_enroll_key_id ?? '') : 'token') },
  { key: 'last_seen', label: 'Last seen', sortable: true, defaultDir: 'desc', format: (a) => fmt(a.last_seen || a.connected_at), hideOnStack: true },
  { key: 'state_changed_at', label: 'Last change', format: (a) => fmt(a.state_changed_at), hideOnStack: true },
]
</script>

<template>
  <UiPage title="Agents" :subtitle="agents.currentVersion ? 'Current agent version ' + agents.currentVersion : ''">
    <template #badges><UiLiveIndicator :connected="live.connected" /></template>
    <template #actions>
      <UiButton variant="text" icon="mdi-refresh" icon-only label="Refresh" @click="reload()" />
      <template v-if="canManage">
        <UiButton v-if="selected.length" variant="soft" icon="mdi-arrow-up-bold-circle-outline" :loading="busyUpgrade" data-test="upgrade-selected" @click="upgradeSelected">Upgrade selected ({{ selected.length }})</UiButton>
        <UiButton variant="soft" icon="mdi-arrow-up-bold-circle" :loading="busyUpgrade" :disabled="!outdatedCount" data-test="upgrade-all" @click="upgradeAll">Upgrade all outdated</UiButton>
        <UiButton icon="mdi-key-plus" data-test="issue-token" @click="openEnroll">Issue enrollment token</UiButton>
      </template>
    </template>
    <UiAlert v-if="message" kind="success" class="mb-3" data-test="upgrade-message">{{ message }}</UiAlert>
    <UiAlert v-if="error || agents.error" kind="error" class="mb-3">{{ error || agents.error }}</UiAlert>
    <UiAlert v-if="manualCount" kind="info" class="mb-3" data-test="manual-upgrade-note">
      {{ manualCount }} agent{{ manualCount === 1 ? ' is' : 's are' }} older than 4.4.0 and cannot upgrade themselves. Install the current agent package on {{ manualCount === 1 ? 'that host' : 'those hosts' }} once by hand; later versions upgrade from here.
    </UiAlert>
    <div class="mb-3 max-w-xs">
      <UiSelect id="fleet-state" v-model="stateFilter" label="Upgrade state" size="sm" :options="stateOptions" data-test="fleet-state-filter" @update:model-value="onStateFilter" />
    </div>
    <UiCard :padded="false">
      <UiDataTable v-model:selected="selected" :items="agents.fleet" :columns="columns" row-key="agent_id" :loading="agents.loading" :total="agents.total" :page="lq.page.value" :page-size="lq.pageSize.value" :sort="lq.sort.value" :selectable="canManage" :row-selectable="canUpgrade" caption="Agents" empty-title="No agents enrolled" :row-attrs="(a) => ({ 'data-test': 'agent-row-' + a.agent_id })" data-test="agents-table" @update:page="lq.setPage" @update:page-size="lq.setPageSize" @update:sort="lq.setSort">
        <template #cell-status="{ row }"><UiStatusChip :status="row.online === false ? 'offline' : 'online'" /></template>
        <template #cell-state="{ row }">
          <span v-if="row.upgrade_state" class="inline-flex flex-col gap-0.5">
            <UiStatusChip :status="row.upgrade_state" :label="fleetStateLabel(row.upgrade_state)" :colors="stateColors" :data-test="'agent-state-' + row.agent_id" />
            <span v-if="row.upgrade_reason" class="text-xs text-base-content/70" :data-test="'agent-reason-' + row.agent_id">{{ reasonText(row.upgrade_reason) }}</span>
          </span>
        </template>
        <template #actions="{ row }">
          <template v-if="canManage">
            <UiButton v-if="canUpgrade(row)" size="xs" variant="soft" icon="mdi-arrow-up-bold" :loading="busyUpgrade" :data-test="'agent-upgrade-' + row.agent_id" @click="upgradeOne(row)">Upgrade</UiButton>
            <UiButton v-if="row.upgrade_state === 'pending' && row.upgrade_id" size="xs" variant="text" :data-test="'agent-cancel-' + row.agent_id" @click="cancel(row)">Cancel</UiButton>
            <UiButton size="xs" variant="soft" icon="mdi-refresh" :loading="busyHost === row.host_id" :disabled="!row.host_id || row.online === false" :data-test="'agent-refresh-' + row.agent_id" @click="refresh(row.host_id)">Refresh</UiButton>
            <UiButton size="xs" variant="text" color="error" :data-test="'agent-revoke-' + row.agent_id" @click="revoke(row)">Revoke</UiButton>
          </template>
        </template>
      </UiDataTable>
    </UiCard>
    <PolicyCard class="mt-4" />
    <AutoEnrollCard v-if="canManage" class="mt-4" />

    <UiDrawer :model-value="enrollOpen" title="Issue enrollment token" size="md" @update:model-value="closeEnroll">
      <template v-if="!minted">
        <p class="mb-3 text-sm text-base-content/70">Mint a single-use, expiring token an agent uses to enroll. The secret is shown once and cannot be recovered.</p>
        <UiForm :form="enrollForm"><UiInput v-bind="enrollForm.field('label')" label="Label (optional)" data-test="enroll-label" /></UiForm>
      </template>
      <template v-else>
        <UiAlert kind="warning" class="mb-3">Copy this token now. It is shown once and will not be displayed again.</UiAlert>
        <UiSecretField id="enroll-token" :model-value="minted.token" label="Enrollment token" readonly data-test="enroll-token" />
        <div class="mt-2 flex items-center justify-between gap-2 text-xs text-base-content/70">
          <span>Expires {{ new Date(minted.expires_at).toLocaleString() }}</span>
          <UiCopyButton :value="minted.token" label="Copy token" />
        </div>
      </template>
      <template #actions>
        <template v-if="!minted">
          <UiButton variant="text" @click="closeEnroll">Cancel</UiButton>
          <UiButton :loading="enrollForm.submitting.value" data-test="enroll-mint" @click="enrollForm.submit()">Mint token</UiButton>
        </template>
        <UiButton v-else @click="closeEnroll">Done</UiButton>
      </template>
    </UiDrawer>
  </UiPage>
</template>
