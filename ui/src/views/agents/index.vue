<script setup lang="ts">
import { onMounted, onUnmounted, ref } from 'vue'
import { UiPage, UiAlert, UiCard, UiButton, UiDataTable, UiStatusChip, UiLiveIndicator, UiForm, UiInput, UiSecretField, UiCopyButton, UiDrawer, useConfirm, type Column } from '@go-tangra/ui'
import { useZodForm } from '@go-tangra/ui/forms'
import { useAgents } from '@/stores/agents'
import { useLive } from '@/stores/live'
import { describe } from '@/api/client'
import { enrollTokenSchema } from '@/schemas'
import type { ConnectedAgent, MintedToken } from '@/api/types'

const agents = useAgents()
const live = useLive()
const confirm = useConfirm()

const enrollOpen = ref(false)
const minted = ref<MintedToken | null>(null)
const message = ref('')
const error = ref('')
const busyHost = ref<string | null>(null)

let release: (() => void) | null = null
onMounted(() => {
  void agents.listConnected()
  release = live.connect()
})
onUnmounted(() => release?.())

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
async function revoke(a: ConnectedAgent): Promise<void> {
  if (!(await confirm.ask({ title: 'Revoke this agent?', text: 'It must enrol again with a new token.', danger: true, confirmLabel: 'Revoke' }))) return
  error.value = ''
  try {
    await agents.revoke(a.agent_id)
  } catch (e) {
    error.value = describe(e)
  }
}
const fmt = (ts?: string): string => (ts ? new Date(ts).toLocaleString() : '')
const columns: Column<ConnectedAgent>[] = [
  { key: 'status', label: 'Status', width: 'sm', format: () => 'online' },
  { key: 'hostname', label: 'Hostname', sortable: true },
  { key: 'agent_id', label: 'Agent ID', format: (a) => a.agent_id.slice(0, 12), hideOnStack: true },
  { key: 'version', label: 'Version', hideOnStack: true },
  { key: 'connected_at', label: 'Connected', format: (a) => fmt(a.connected_at) },
]
</script>

<template>
  <UiPage title="Agents">
    <template #badges><UiLiveIndicator :connected="live.connected" /></template>
    <template #actions>
      <UiButton variant="text" icon="mdi-refresh" icon-only label="Refresh" @click="agents.listConnected()" />
      <UiButton icon="mdi-key-plus" data-test="issue-token" @click="openEnroll">Issue enrollment token</UiButton>
    </template>
    <UiAlert v-if="message" kind="success" class="mb-3">{{ message }}</UiAlert>
    <UiAlert v-if="error || agents.error" kind="error" class="mb-3">{{ error || agents.error }}</UiAlert>
    <UiCard :padded="false">
      <UiDataTable :items="agents.connected" :columns="columns" row-key="agent_id" :loading="agents.loading" caption="Connected agents" empty-title="No agents connected" :row-attrs="(a) => ({ 'data-test': 'agent-row-' + a.agent_id })" data-test="agents-table">
        <template #cell-status><UiStatusChip status="online" /></template>
        <template #actions="{ row }">
          <UiButton size="xs" variant="soft" icon="mdi-refresh" :loading="busyHost === row.host_id" :disabled="!row.host_id" :data-test="'agent-refresh-' + row.agent_id" @click="refresh(row.host_id)">Refresh</UiButton>
          <UiButton size="xs" variant="text" color="error" :data-test="'agent-revoke-' + row.agent_id" @click="revoke(row)">Revoke</UiButton>
        </template>
      </UiDataTable>
    </UiCard>

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
