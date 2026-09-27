<script setup lang="ts">
import { computed, onMounted, ref } from 'vue'
import { useAbility } from '@casl/vue'
import { UiAlert, UiButton, UiCard, UiForm, UiInput, UiNumberInput, UiSwitch } from '@go-tangra/ui'
import { useZodForm } from '@go-tangra/ui/forms'
import { useAgents } from '@/stores/agents'
import { describe } from '@/api/client'
import { upgradePolicySchema } from '@/schemas'

// Automatic upgrades are a fleet-wide code change: editing needs
// {manage, InventoryAgentUpgradePolicy} (agentupgrades:manage); everyone who
// sees the agent list reads the policy.
const agents = useAgents()
const ability = useAbility()
const canManage = computed(() => ability.can('manage', 'InventoryAgentUpgradePolicy'))
const message = ref('')
const error = ref('')
const resuming = ref(false)

const form = useZodForm(upgradePolicySchema, {
  onSubmit: (v) => agents.savePolicy(v),
  onSuccess: () => (message.value = 'Upgrade policy saved.'),
})
function resetForm(): void {
  const p = agents.policy
  if (p) form.reset({ enabled: p.enabled, window_start: p.window_start, window_end: p.window_end, timezone: p.timezone, max_concurrent: p.max_concurrent, target_version: p.target_version })
}
onMounted(async () => {
  try {
    await agents.loadPolicy()
    resetForm()
  } catch (e) {
    error.value = describe(e)
  }
})

async function resume(): Promise<void> {
  resuming.value = true
  message.value = ''
  error.value = ''
  try {
    await agents.resumePolicy()
    message.value = 'Automatic upgrades resumed.'
  } catch (e) {
    error.value = describe(e)
  } finally {
    resuming.value = false
  }
}
const pausedBy = computed(() => (agents.policy?.paused_reason ?? '').split(':')[0] === 'rolled_back' ? 'rolled back' : 'failed')
</script>

<template>
  <UiCard title="Automatic upgrades" data-test="policy-card">
    <UiAlert v-if="error" kind="error" class="mb-3">{{ error }}</UiAlert>
    <UiAlert v-if="message" kind="success" class="mb-3" data-test="policy-message">{{ message }}</UiAlert>
    <template v-if="agents.policy">
      <UiAlert v-if="agents.policy.paused" kind="warning" class="mb-3" data-test="policy-paused">
        Automatic upgrades are paused: an automatic upgrade {{ pausedBy }}. Check the agent, then resume.
        <UiButton v-if="canManage" size="xs" variant="soft" class="ml-2" :loading="resuming" data-test="policy-resume" @click="resume">Resume</UiButton>
      </UiAlert>
      <p class="mb-3 text-sm text-base-content/70">
        When enabled, outdated agents that support self-upgrade are upgraded inside the maintenance window, never more than the set number at once.
        A pinned version (also lower than the current one) applies to every upgrade of this tenant.
      </p>
      <UiForm :form="form">
        <div class="grid gap-3 sm:grid-cols-2">
          <UiSwitch v-bind="form.field('enabled')" label="Upgrade agents automatically" :disabled="!canManage" data-test="policy-enabled" />
          <UiNumberInput v-bind="form.field('max_concurrent')" label="Concurrent upgrades" :min="1" :max="100" :disabled="!canManage" required />
          <UiInput v-bind="form.field('window_start')" label="Window start (HH:MM)" :disabled="!canManage" required />
          <UiInput v-bind="form.field('window_end')" label="Window end (HH:MM)" hint="Equal to the start: the whole day" :disabled="!canManage" required />
          <UiInput v-bind="form.field('timezone')" label="Timezone" hint="IANA name, e.g. Europe/Sofia" :disabled="!canManage" required />
          <UiInput v-bind="form.field('target_version')" label="Pinned version (optional)" hint="Empty: the current agent version" :disabled="!canManage" />
        </div>
      </UiForm>
      <div v-if="canManage" class="mt-3 flex justify-end gap-2">
        <UiButton variant="text" @click="resetForm">Reset</UiButton>
        <UiButton :loading="form.submitting.value" data-test="policy-save" @click="form.submit()">Save</UiButton>
      </div>
      <p v-else class="mt-3 text-sm text-base-content/70" data-test="policy-readonly">Only inventory administrators can change automatic upgrades.</p>
    </template>
  </UiCard>
</template>
