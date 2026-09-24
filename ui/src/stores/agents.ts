import { defineStore } from 'pinia'
import { ref } from 'vue'
import { api } from '@/api/client'
import type { ConnectedAgent, MintedToken, RefreshResult } from '@/api/types'

export const useAgents = defineStore('inventory-agents', () => {
  // Connected agents keyed by host_id for O(1) live patching.
  const connected = ref<ConnectedAgent[]>([])
  const loading = ref(false)
  const error = ref('')

  async function listConnected(): Promise<void> {
    loading.value = true
    error.value = ''
    try {
      const res = await api<{ items: ConnectedAgent[] }>('GET', 'agents')
      connected.value = res.items ?? []
    } catch (e) {
      error.value = (e as Error).message
    } finally {
      loading.value = false
    }
  }

  // refresh triggers an on-demand collection on the host's agent.
  async function refresh(hostId: string): Promise<RefreshResult> {
    return api<RefreshResult>('POST', 'agents/' + hostId + '/refresh')
  }

  // mintEnrollToken mints a tenant-scoped enrollment token; the secret is
  // returned ONCE and never again.
  async function mintEnrollToken(label?: string): Promise<MintedToken> {
    return api<MintedToken>('POST', 'agents/enroll-token', label ? { label } : {})
  }

  async function revoke(id: string): Promise<void> {
    await api('POST', 'agents/' + id + '/revoke')
    connected.value = connected.value.filter((a) => a.agent_id !== id)
  }

  // patchOnline / patchOffline reflect live registry events without a refetch.
  function patchOnline(agent: ConnectedAgent): void {
    const i = connected.value.findIndex((a) => a.agent_id === agent.agent_id)
    if (i >= 0) connected.value[i] = agent
    else connected.value = [agent, ...connected.value]
  }

  function patchOffline(agentId: string): void {
    connected.value = connected.value.filter((a) => a.agent_id !== agentId)
  }

  return { connected, loading, error, listConnected, refresh, mintEnrollToken, revoke, patchOnline, patchOffline }
})
