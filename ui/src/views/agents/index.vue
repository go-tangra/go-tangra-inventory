<script setup lang="ts">
import { onMounted, onUnmounted, ref } from 'vue'
import { useAgents } from '@/stores/agents'
import { useLive } from '@/stores/live'
import { describe } from '@/api/client'
import EnrollTokenDialog from '@/components/EnrollTokenDialog.vue'

const agents = useAgents()
const live = useLive()

const enrollOpen = ref(false)
const message = ref('')
const error = ref('')
const busyHost = ref<string | null>(null)

let release: (() => void) | null = null

onMounted(() => {
  void agents.listConnected()
  release = live.connect()
})
onUnmounted(() => release?.())

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

async function revoke(agentId: string): Promise<void> {
  error.value = ''
  try {
    await agents.revoke(agentId)
  } catch (e) {
    error.value = describe(e)
  }
}

function fmt(ts?: string): string {
  return ts ? new Date(ts).toLocaleString() : '—'
}
</script>

<template>
  <div>
    <div class="d-flex align-center mb-4">
      <h1 class="text-h5">Agents</h1>
      <v-chip v-if="live.connected" size="x-small" color="success" variant="tonal" class="ms-3">live</v-chip>
      <v-spacer />
      <v-btn variant="text" icon="mdi-refresh" class="me-2" @click="agents.listConnected()" />
      <v-btn color="primary" prepend-icon="mdi-key-plus" data-test="issue-token" @click="enrollOpen = true">Issue enrollment token</v-btn>
    </div>

    <v-alert v-if="message" type="success" variant="tonal" density="compact" class="mb-3">{{ message }}</v-alert>
    <v-alert v-if="error || agents.error" type="error" variant="tonal" density="compact" class="mb-3">{{ error || agents.error }}</v-alert>

    <v-table data-test="agents-table">
      <thead>
        <tr><th>Status</th><th>Hostname</th><th>Agent ID</th><th>Version</th><th>Connected</th><th class="text-right">Actions</th></tr>
      </thead>
      <tbody>
        <tr v-for="a in agents.connected" :key="a.agent_id" :data-test="'agent-row-' + a.agent_id">
          <td><v-chip size="x-small" color="success" variant="tonal" prepend-icon="mdi-circle">online</v-chip></td>
          <td>{{ a.hostname || '—' }}</td>
          <td class="text-medium-emphasis">{{ a.agent_id.slice(0, 12) }}</td>
          <td class="text-medium-emphasis">{{ a.version || '—' }}</td>
          <td class="text-medium-emphasis">{{ fmt(a.connected_at) }}</td>
          <td class="text-right">
            <v-btn size="x-small" variant="tonal" prepend-icon="mdi-refresh" :loading="busyHost === a.host_id" :disabled="!a.host_id" :data-test="'agent-refresh-' + a.agent_id" @click="refresh(a.host_id)">Refresh</v-btn>
            <v-btn size="x-small" variant="text" color="error" class="ms-2" :data-test="'agent-revoke-' + a.agent_id" @click="revoke(a.agent_id)">Revoke</v-btn>
          </td>
        </tr>
        <tr v-if="!agents.connected.length && !agents.loading">
          <td colspan="6" class="text-medium-emphasis">No agents connected.</td>
        </tr>
      </tbody>
    </v-table>

    <EnrollTokenDialog v-model="enrollOpen" />
  </div>
</template>
