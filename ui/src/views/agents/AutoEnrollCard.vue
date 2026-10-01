<script setup lang="ts">
// Automatic enrollment (feature 029): agents enroll without a token by
// proving possession of a tenant key, only from the key's networks. The
// secret is shown once, at creation or rotation; afterwards only the public
// key id is visible. Managed with agents:manage like tokens.
import { computed, onMounted, ref, watch } from 'vue'
import { UiAlert, UiButton, UiCard, UiCopyButton, UiDataTable, UiDateInput, UiDrawer, UiForm, UiInput, UiNumberInput, UiSecretField, UiStatusChip, UiSwitch, UiTextarea, useConfirm, useListQuery, type Column } from '@go-tangra/ui'
import { useZodForm } from '@go-tangra/ui/forms'
import { AUTO_KEY_LIST, useAgents } from '@/stores/agents'
import { ApiError, describe } from '@/api/client'
import { autoEnrollKeySchema, parseNetworks } from '@/schemas'
import type { AutoEnrollKey, AutoEnrollKeySecret } from '@/api/types'

const agents = useAgents()
const confirm = useConfirm()
const error = ref('')
const message = ref('')
const switching = ref(false)
const busyKey = ref('')
const drawer = ref(false)
const editing = ref<AutoEnrollKey | null>(null)
const issued = ref<AutoEnrollKeySecret | null>(null)

const settings = computed(() => agents.autoEnroll)

// --- server paging and sorting of the keys (?autokeys.page=…) ---
const lq = useListQuery('autokeys', AUTO_KEY_LIST)
async function load(): Promise<void> {
  try {
    const res = await agents.loadAutoEnroll(lq.query.value)
    if (res?.page) lq.clampTo(res.page) // a page beyond the end answers the last page
  } catch (e) {
    error.value = describe(e)
  }
}
onMounted(load)
watch(lq.query, () => void load())

async function toggle(on: boolean): Promise<void> {
  switching.value = true
  error.value = ''
  message.value = ''
  try {
    await agents.setAutoEnroll(on)
    message.value = on ? 'Automatic enrollment is on.' : 'Automatic enrollment is off. Agents can no longer enroll with a key.'
  } catch (e) {
    error.value = describe(e)
  } finally {
    switching.value = false
  }
}

// Server field names -> form field names.
const serverField: Record<string, string> = { name: 'name', allowed_cidrs: 'networks', expires_at: 'expires_on', max_enrollments: 'max_enrollments' }

const endOfDay = (d: string): string | null => (d ? new Date(d + 'T23:59:59').toISOString() : null)
const dateOnly = (ts: string | null): string => {
  if (!ts) return ''
  const d = new Date(ts)
  return `${d.getFullYear()}-${String(d.getMonth() + 1).padStart(2, '0')}-${String(d.getDate()).padStart(2, '0')}`
}

const form = useZodForm(autoEnrollKeySchema, {
  initial: { name: '', networks: '', expires_on: '', max_enrollments: 0 },
  onSubmit: async (v) => {
    const input = { name: v.name, allowed_cidrs: parseNetworks(v.networks), expires_at: endOfDay(v.expires_on), max_enrollments: v.max_enrollments }
    try {
      if (editing.value) {
        await agents.updateAutoKey(editing.value.id, input)
        drawer.value = false
        message.value = `Key "${v.name}" saved.`
      } else {
        issued.value = await agents.createAutoKey(input)
      }
    } catch (e) {
      const f = e instanceof ApiError ? serverField[String(e.detail?.field ?? '')] : undefined
      if (f) {
        form.setFieldError(f, String(e instanceof ApiError ? (e.detail?.message ?? e.reason) : ''))
        return
      }
      throw e
    }
  },
})

function openCreate(): void {
  editing.value = null
  issued.value = null
  form.reset({ name: '', networks: '', expires_on: '', max_enrollments: 0 })
  drawer.value = true
}
function openEdit(k: AutoEnrollKey): void {
  editing.value = k
  issued.value = null
  form.reset({ name: k.name, networks: k.allowed_cidrs.join('\n'), expires_on: dateOnly(k.expires_at), max_enrollments: k.max_enrollments })
  drawer.value = true
}
function closeDrawer(): void {
  drawer.value = false
  issued.value = null
}

async function run(k: AutoEnrollKey, action: () => Promise<void>): Promise<void> {
  busyKey.value = k.id
  error.value = ''
  message.value = ''
  try {
    await action()
  } catch (e) {
    error.value = describe(e)
  } finally {
    busyKey.value = ''
  }
}
const setEnabled = (k: AutoEnrollKey, on: boolean): Promise<void> =>
  run(k, async () => {
    await agents.updateAutoKey(k.id, { enabled: on })
    message.value = `Key "${k.name}" ${on ? 'enabled' : 'disabled'}.`
  })
async function rotate(k: AutoEnrollKey): Promise<void> {
  if (!(await confirm.ask({ title: `Rotate the secret of "${k.name}"?`, text: 'Enrolled agents keep working. New enrollments need the new secret; the old one stops working at once.', confirmLabel: 'Rotate' }))) return
  await run(k, async () => {
    issued.value = await agents.rotateAutoKey(k.id)
    editing.value = k
    drawer.value = true
  })
}
async function remove(k: AutoEnrollKey): Promise<void> {
  if (!(await confirm.ask({ title: `Delete the key "${k.name}"?`, text: 'Agents enrolled with it keep working. Nothing can enroll with it any more.', danger: true, confirmLabel: 'Delete' }))) return
  await run(k, async () => {
    await agents.deleteAutoKey(k.id)
    message.value = `Key "${k.name}" deleted.`
  })
}

const stateColors = { active: 'success', disabled: 'neutral', expired: 'warning', exhausted: 'warning' } as const
const fmt = (ts: string | null): string => (ts ? new Date(ts).toLocaleString() : '')
// Sortable columns are the server's sort fields (AUTO_KEY_LIST).
const columns: Column<AutoEnrollKey>[] = [
  { key: 'name', label: 'Name', sortable: true },
  { key: 'key_id', label: 'Key ID', hideOnStack: true },
  { key: 'allowed_cidrs', label: 'Networks', format: (k) => k.allowed_cidrs.join(', ') },
  { key: 'state', label: 'State', width: 'sm' },
  { key: 'enrollments', label: 'Enrolled', width: 'sm', format: (k) => (k.max_enrollments ? `${k.enrollments} / ${k.max_enrollments}` : String(k.enrollments)) },
  { key: 'expires_at', label: 'Expires', hideOnStack: true, format: (k) => fmt(k.expires_at) || 'never' },
  { key: 'last_used_at', label: 'Last used', hideOnStack: true, format: (k) => (k.last_used_at ? `${fmt(k.last_used_at)} (${k.last_used_ip})` : '') },
  { key: 'created_at', label: 'Created', hideOnStack: true, sortable: true, defaultDir: 'desc', format: (k) => fmt(k.created_at) },
]

const keyFile = '/etc/inventory-agent/auto-enroll.key'
const configSnippet = computed(() =>
  issued.value ? `credential_file: /var/lib/inventory-agent/credential\nstate_file: /var/lib/inventory-agent/state\nauto_enroll:\n  key_id: ${issued.value.key.key_id}\n  key_file: ${keyFile}` : '',
)
const cliSnippet = computed(() => (issued.value ? `inventory-agent -daemon -ingest <host>:9977 -auto-enroll-key-id ${issued.value.key.key_id} -auto-enroll-key-file ${keyFile}` : ''))
</script>

<template>
  <UiCard title="Automatic enrollment" data-test="auto-enroll-card">
    <UiAlert v-if="error" kind="error" class="mb-3">{{ error }}</UiAlert>
    <UiAlert v-if="message" kind="success" class="mb-3" data-test="auto-enroll-message">{{ message }}</UiAlert>
    <template v-if="settings">
      <p class="mb-3 text-sm text-base-content/70">
        Agents (for example deployed by go-tangra-client) can enroll without a single-use token by proving that they hold an enrollment key. The key itself is never sent:
        the agent signs its request, which is accepted only from the key's networks, within {{ Math.round(settings.window_seconds / 60) }} minutes of the agent's clock and only once.
      </p>
      <div class="mb-3 flex flex-wrap items-center justify-between gap-3">
        <UiSwitch id="auto-enroll-enabled" :model-value="settings.enabled" label="Allow agents to enroll with a key" :disabled="switching" data-test="auto-enroll-enabled" @update:model-value="toggle" />
        <UiButton icon="mdi-key-plus" data-test="auto-enroll-new" @click="openCreate">New key</UiButton>
      </div>
      <UiAlert v-if="!settings.enabled && (settings.total ?? settings.keys.length)" kind="info" class="mb-3" data-test="auto-enroll-off">Automatic enrollment is off: no key can be used until it is switched on.</UiAlert>
      <UiDataTable :items="settings.keys" :columns="columns" row-key="id" :total="settings.total ?? settings.keys.length" :page="lq.page.value" :page-size="lq.pageSize.value" :sort="lq.sort.value" caption="Enrollment keys" empty-title="No enrollment keys" :row-attrs="(k) => ({ 'data-test': 'auto-key-' + k.key_id })" data-test="auto-enroll-keys" @update:page="lq.setPage" @update:page-size="lq.setPageSize" @update:sort="lq.setSort">
        <template #cell-key_id="{ row }"><code class="text-xs">{{ row.key_id }}</code></template>
        <template #cell-state="{ row }"><UiStatusChip :status="row.state" :colors="stateColors" /></template>
        <template #actions="{ row }">
          <UiButton size="xs" variant="soft" :loading="busyKey === row.id" :data-test="'auto-key-toggle-' + row.key_id" @click="setEnabled(row, !row.enabled)">{{ row.enabled ? 'Disable' : 'Enable' }}</UiButton>
          <UiButton size="xs" variant="text" icon="mdi-pencil-outline" icon-only label="Edit" :data-test="'auto-key-edit-' + row.key_id" @click="openEdit(row)" />
          <UiButton size="xs" variant="text" icon="mdi-autorenew" icon-only label="Rotate secret" :data-test="'auto-key-rotate-' + row.key_id" @click="rotate(row)" />
          <UiButton size="xs" variant="text" color="error" icon="mdi-delete-outline" icon-only label="Delete" :data-test="'auto-key-delete-' + row.key_id" @click="remove(row)" />
        </template>
      </UiDataTable>
    </template>

    <UiDrawer :model-value="drawer" :title="issued ? 'Enrollment key secret' : editing ? 'Edit enrollment key' : 'New enrollment key'" size="md" @update:model-value="closeDrawer">
      <template v-if="issued">
        <UiAlert kind="warning" class="mb-3">Copy the secret now. It is shown once and cannot be recovered; store it only where agents are installed.</UiAlert>
        <UiInput id="auto-key-id" :model-value="issued.key.key_id" label="Key ID" readonly data-test="auto-key-id" />
        <UiSecretField id="auto-key-secret" :model-value="issued.secret" label="Secret" readonly class="mt-3" data-test="auto-key-secret" />
        <div class="mt-2 flex justify-end"><UiCopyButton :value="issued.secret" label="Copy secret" /></div>
        <p class="mt-4 text-sm text-base-content/70">Write the secret to <code>{{ keyFile }}</code> (readable by the agent only), then configure the agent:</p>
        <pre class="mt-2 overflow-x-auto rounded-box bg-base-200 p-3 text-xs" data-test="auto-key-config">{{ configSnippet }}</pre>
        <div class="mt-1 flex justify-end"><UiCopyButton :value="configSnippet" label="Copy configuration" /></div>
        <p class="mt-3 text-sm text-base-content/70">or pass it on the command line:</p>
        <pre class="mt-2 overflow-x-auto rounded-box bg-base-200 p-3 text-xs">{{ cliSnippet }}</pre>
      </template>
      <UiForm v-else :form="form">
        <div class="flex flex-col gap-3">
          <UiInput v-bind="form.field('name')" label="Name" required data-test="auto-key-name" />
          <UiTextarea v-bind="form.field('networks')" label="Allowed networks" hint="One per line, e.g. 10.20.0.0/16 or 192.0.2.10; agents enroll only from these addresses" :rows="4" required data-test="auto-key-networks" />
          <UiDateInput v-bind="form.field('expires_on')" label="Expires on (optional)" hint="Empty: never" data-test="auto-key-expires" />
          <UiNumberInput v-bind="form.field('max_enrollments')" label="Enrollment limit" hint="0: unlimited" :min="0" :max="1000000" data-test="auto-key-max" />
        </div>
      </UiForm>
      <template #actions>
        <UiButton v-if="issued" data-test="auto-key-done" @click="closeDrawer">Done</UiButton>
        <template v-else>
          <UiButton variant="text" @click="closeDrawer">Cancel</UiButton>
          <UiButton :loading="form.submitting.value" data-test="auto-key-save" @click="form.submit()">{{ editing ? 'Save' : 'Create key' }}</UiButton>
        </template>
      </template>
    </UiDrawer>
  </UiCard>
</template>
