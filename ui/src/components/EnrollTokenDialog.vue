<script setup lang="ts">
import { ref, watch } from 'vue'
import { useAgents } from '@/stores/agents'
import { describe } from '@/api/client'
import type { MintedToken } from '@/api/types'

const props = defineProps<{ modelValue: boolean }>()
const emit = defineEmits<{ 'update:modelValue': [boolean] }>()

const agents = useAgents()

const label = ref('')
const error = ref('')
const busy = ref(false)
const minted = ref<MintedToken | null>(null)
const copied = ref(false)

watch(
  () => props.modelValue,
  (open) => {
    if (!open) return
    label.value = ''
    error.value = ''
    minted.value = null
    copied.value = false
  },
)

async function mint(): Promise<void> {
  busy.value = true
  error.value = ''
  try {
    minted.value = await agents.mintEnrollToken(label.value.trim() || undefined)
  } catch (e) {
    error.value = describe(e)
  } finally {
    busy.value = false
  }
}

async function copy(): Promise<void> {
  if (!minted.value) return
  try {
    await navigator.clipboard.writeText(minted.value.token)
    copied.value = true
  } catch {
    /* clipboard denied; the token stays visible for manual copy */
  }
}
</script>

<template>
  <v-dialog :model-value="modelValue" max-width="560" @update:model-value="emit('update:modelValue', $event)">
    <v-card>
      <v-card-title class="text-subtitle-1">Issue enrollment token</v-card-title>
      <v-card-text>
        <v-alert v-if="error" type="error" variant="tonal" density="compact" class="mb-3">{{ error }}</v-alert>

        <template v-if="!minted">
          <p class="text-body-2 text-medium-emphasis mb-3">
            Mint a single-use, expiring token an agent uses to enroll. The secret is shown once and cannot be recovered.
          </p>
          <v-text-field v-model="label" label="Label (optional)" density="compact" hide-details data-test="enroll-label" />
        </template>

        <template v-else>
          <v-alert type="warning" variant="tonal" density="compact" class="mb-3">
            Copy this token now. It is shown once and will not be displayed again.
          </v-alert>
          <v-textarea
            :model-value="minted.token"
            label="Enrollment token"
            readonly
            rows="3"
            auto-grow
            density="compact"
            hide-details
            data-test="enroll-token"
            class="mb-2"
          />
          <div class="text-caption text-medium-emphasis">Expires {{ new Date(minted.expires_at).toLocaleString() }}</div>
          <v-chip v-if="copied" size="x-small" color="success" variant="tonal" class="mt-2">copied</v-chip>
        </template>
      </v-card-text>
      <v-card-actions>
        <v-spacer />
        <template v-if="!minted">
          <v-btn variant="text" @click="emit('update:modelValue', false)">Cancel</v-btn>
          <v-btn color="primary" :loading="busy" data-test="enroll-mint" @click="mint">Mint token</v-btn>
        </template>
        <template v-else>
          <v-btn variant="tonal" prepend-icon="mdi-content-copy" @click="copy">Copy</v-btn>
          <v-btn color="primary" @click="emit('update:modelValue', false)">Done</v-btn>
        </template>
      </v-card-actions>
    </v-card>
  </v-dialog>
</template>
