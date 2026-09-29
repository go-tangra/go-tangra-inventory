import { defineStore } from 'pinia'
import { computed, ref } from 'vue'
import { api } from '@/api/client'
import type { AutoEnroll, AutoEnrollKey, AutoEnrollKeyInput, AutoEnrollKeySecret, AgentFleet, AgentFleetEntry, AgentReleaseList, AgentUpgrade, ConnectedAgent, FleetState, MintedToken, RefreshResult, UpgradeBatchResult, UpgradePolicy } from '@/api/types'

export interface FleetFilter {
  upgrade_state?: FleetState
  outdated?: boolean
}

export const useAgents = defineStore('inventory-agents', () => {
  // Every enrolled agent (online and offline) with its upgrade state. A
  // legacy listing carries no `online` key: every entry there is connected.
  const fleet = ref<AgentFleetEntry[]>([])
  const currentVersion = ref('')
  const loading = ref(false)
  const error = ref('')
  const connected = computed<ConnectedAgent[]>(() => fleet.value.filter((a) => a.online !== false))

  async function listFleet(filter: FleetFilter = {}): Promise<void> {
    loading.value = true
    error.value = ''
    try {
      const q = new URLSearchParams()
      if (filter.upgrade_state) q.set('state', filter.upgrade_state)
      if (filter.outdated) q.set('outdated', 'true')
      const qs = q.toString()
      const res = await api<AgentFleet>('GET', 'agents' + (qs ? '?' + qs : ''))
      fleet.value = res.items ?? []
      currentVersion.value = res.current_version ?? ''
    } catch (e) {
      error.value = (e as Error).message
    } finally {
      loading.value = false
    }
  }

  // listConnected keeps the former name for the host and dashboard views.
  const listConnected = (): Promise<void> => listFleet()

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
    fleet.value = fleet.value.filter((a) => a.agent_id !== id)
  }

  // upgrade asks the listed agents to upgrade to the tenant target version.
  async function upgrade(agentIds: string[]): Promise<UpgradeBatchResult> {
    return api<UpgradeBatchResult>('POST', 'agents/upgrades', { agent_ids: agentIds })
  }

  // upgradeAllOutdated asks every outdated, upgrade-capable agent to upgrade.
  async function upgradeAllOutdated(): Promise<UpgradeBatchResult> {
    return api<UpgradeBatchResult>('POST', 'agents/upgrades', { all_outdated: true })
  }

  async function cancelUpgrade(upgradeId: string): Promise<AgentUpgrade> {
    return api<AgentUpgrade>('POST', 'agents/upgrades/' + upgradeId + '/cancel')
  }

  async function releases(): Promise<AgentReleaseList> {
    return api<AgentReleaseList>('GET', 'agent-releases')
  }

  // The automatic upgrade policy: read with agents:manage, changed with agentupgrades:manage.
  const policy = ref<UpgradePolicy | null>(null)
  async function loadPolicy(): Promise<void> {
    policy.value = await api<UpgradePolicy>('GET', 'agents/upgrade-policy')
  }
  async function savePolicy(p: Omit<UpgradePolicy, 'paused' | 'paused_reason' | 'updated_by' | 'updated_at'>): Promise<void> {
    policy.value = await api<UpgradePolicy>('PUT', 'agents/upgrade-policy', p)
  }
  async function resumePolicy(): Promise<void> {
    policy.value = await api<UpgradePolicy>('POST', 'agents/upgrade-policy/resume')
  }

  // Automatic enrollment (feature 029): the tenant switch and its keys.
  const autoEnroll = ref<AutoEnroll | null>(null)
  async function loadAutoEnroll(): Promise<void> {
    autoEnroll.value = await api<AutoEnroll>('GET', 'agents/auto-enroll')
  }
  async function setAutoEnroll(enabled: boolean): Promise<void> {
    await api('PUT', 'agents/auto-enroll', { enabled })
    await loadAutoEnroll()
  }
  async function createAutoKey(input: AutoEnrollKeyInput): Promise<AutoEnrollKeySecret> {
    const res = await api<AutoEnrollKeySecret>('POST', 'agents/auto-enroll/keys', input)
    await loadAutoEnroll()
    return res
  }
  async function updateAutoKey(id: string, patch: Partial<AutoEnrollKeyInput> & { enabled?: boolean }): Promise<AutoEnrollKey> {
    const res = await api<AutoEnrollKey>('PATCH', 'agents/auto-enroll/keys/' + id, patch)
    await loadAutoEnroll()
    return res
  }
  async function rotateAutoKey(id: string): Promise<AutoEnrollKeySecret> {
    const res = await api<AutoEnrollKeySecret>('POST', 'agents/auto-enroll/keys/' + id + '/rotate')
    await loadAutoEnroll()
    return res
  }
  async function deleteAutoKey(id: string): Promise<void> {
    await api('DELETE', 'agents/auto-enroll/keys/' + id)
    await loadAutoEnroll()
  }

  // patchOnline / patchOffline reflect live registry events without a refetch.
  function patchOnline(agent: ConnectedAgent): void {
    const i = fleet.value.findIndex((a) => a.agent_id === agent.agent_id)
    if (i >= 0) fleet.value[i] = { ...fleet.value[i], ...agent, online: true }
    else fleet.value = [{ ...agent, online: true }, ...fleet.value]
  }

  function patchOffline(agentId: string): void {
    const i = fleet.value.findIndex((a) => a.agent_id === agentId)
    if (i >= 0) fleet.value[i] = { ...fleet.value[i]!, online: false }
  }

  return { fleet, connected, currentVersion, loading, error, listFleet, listConnected, refresh, mintEnrollToken, revoke, upgrade, upgradeAllOutdated, cancelUpgrade, releases, policy, loadPolicy, savePolicy, resumePolicy, autoEnroll, loadAutoEnroll, setAutoEnroll, createAutoKey, updateAutoKey, rotateAutoKey, deleteAutoKey, patchOnline, patchOffline }
})
