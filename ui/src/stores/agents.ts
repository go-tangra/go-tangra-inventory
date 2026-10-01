import { defineStore } from 'pinia'
import { ref } from 'vue'
import type { ListQueryOptions } from '@go-tangra/ui'
import { api } from '@/api/client'
import type { AutoEnroll, AutoEnrollKey, AutoEnrollKeyInput, AutoEnrollKeySecret, AgentFleet, AgentFleetEntry, AgentReleaseList, AgentUpgrade, ConnectedAgent, FleetState, ListParams, MintedToken, RefreshResult, UpgradeBatchResult, UpgradePolicy } from '@/api/types'

export const PAGE_SIZE = 25
/** The largest page the server answers (listquery.MaxPageSize). */
const MAX_PAGE_SIZE = 200
/** Upper bound of pages read to collect every connected agent. */
const MAX_ONLINE_PAGES = 50

/** Sortable fields of GET /agents (server Spec store.FleetList; state is the upgrade state). */
export const FLEET_SORTS = ['hostname', 'version', 'state', 'last_seen'] as const
export const FLEET_LIST: ListQueryOptions = { sortable: [...FLEET_SORTS], defaultSort: { key: 'hostname', dir: 'asc' }, defaultSize: PAGE_SIZE }
const FLEET_FIRST: ListParams = { page: 1, page_size: PAGE_SIZE, sort: 'hostname', order: 'asc' }

/** Sortable fields of the auto-enrollment keys (server Spec store.AutoEnrollKeyList). */
export const AUTO_KEY_SORTS = ['name', 'created_at'] as const
export const AUTO_KEY_LIST: ListQueryOptions = { sortable: [...AUTO_KEY_SORTS], defaultSort: { key: 'name', dir: 'asc' }, defaultSize: PAGE_SIZE }
const AUTO_KEY_FIRST: ListParams = { page: 1, page_size: PAGE_SIZE, sort: 'name', order: 'asc' }

export interface FleetFilter {
  upgrade_state?: FleetState
  outdated?: boolean
}

function fleetQuery(f: FleetFilter): Record<string, string> {
  const q: Record<string, string> = {}
  if (f.upgrade_state) q.state = f.upgrade_state
  if (f.outdated) q.outdated = 'true'
  return q
}

export const useAgents = defineStore('inventory-agents', () => {
  // One server page of enrolled agents (online and offline) with their
  // upgrade state, in the order of `params`.
  const fleet = ref<AgentFleetEntry[]>([])
  const total = ref(0)
  const params = ref<ListParams>({ ...FLEET_FIRST })
  const filter = ref<FleetFilter>({})
  const currentVersion = ref('')
  const loading = ref(false)
  const error = ref('')
  let seq = 0

  /**
   * Loads one server page of the fleet. Resolves with the page, or null when
   * it failed or a newer request superseded it.
   */
  async function listFleet(f: FleetFilter = filter.value, q: ListParams = params.value): Promise<AgentFleet | null> {
    const mine = ++seq
    loading.value = true
    error.value = ''
    filter.value = { ...f }
    params.value = { ...q }
    try {
      const res = await api<AgentFleet>('GET', 'agents', undefined, { query: { ...fleetQuery(f), ...q } })
      if (mine !== seq) return null
      fleet.value = res.items ?? []
      total.value = res.total ?? fleet.value.length
      currentVersion.value = res.current_version ?? ''
      return res
    } catch (e) {
      if (mine === seq) error.value = (e as Error).message
      return null
    } finally {
      if (mine === seq) loading.value = false
    }
  }

  /** The number of agents matching f (a one-row page; 0 on failure). */
  async function countFleet(f: FleetFilter): Promise<number> {
    try {
      const res = await api<AgentFleet>('GET', 'agents', undefined, { query: { ...fleetQuery(f), page: 1, page_size: 1 } })
      return res.total ?? 0
    } catch {
      return 0
    }
  }

  // Every connected agent of the tenant (host and dashboard views), read
  // page by page; kept current by live registry events.
  const connected = ref<AgentFleetEntry[]>([])
  async function listConnected(): Promise<void> {
    const all: AgentFleetEntry[] = []
    try {
      for (let page = 1; page <= MAX_ONLINE_PAGES; page++) {
        const res = await api<AgentFleet>('GET', 'agents', undefined, { query: { online: 'true', page, page_size: MAX_PAGE_SIZE, sort: 'hostname', order: 'asc' } })
        const items = (res.items ?? []).filter((a) => a.online !== false)
        all.push(...items)
        if (!res.items?.length || (res.page ?? page) < page || page * MAX_PAGE_SIZE >= (res.total ?? 0)) break
      }
      connected.value = all
    } catch (e) {
      error.value = (e as Error).message
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
    fleet.value = fleet.value.filter((a) => a.agent_id !== id)
    connected.value = connected.value.filter((a) => a.agent_id !== id)
    if (total.value > 0) total.value--
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
  // The keys are one server page in the order of `autoKeyParams`.
  const autoEnroll = ref<AutoEnroll | null>(null)
  const autoKeyParams = ref<ListParams>({ ...AUTO_KEY_FIRST })
  let autoSeq = 0
  /** Loads the switch and one page of keys; resolves with the response (null when superseded). */
  async function loadAutoEnroll(q: ListParams = autoKeyParams.value): Promise<AutoEnroll | null> {
    const mine = ++autoSeq
    autoKeyParams.value = { ...q }
    const res = await api<AutoEnroll>('GET', 'agents/auto-enroll', undefined, { query: { ...q } })
    if (mine !== autoSeq) return null
    const keys = Array.isArray(res?.keys) ? res.keys : []
    autoEnroll.value = { ...res, enabled: res?.enabled === true, window_seconds: res?.window_seconds ?? 300, keys, total: res?.total ?? keys.length }
    return autoEnroll.value
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
  // The fleet page is server-ordered: an agent not on it is not inserted.
  function patchOnline(agent: ConnectedAgent): void {
    const i = fleet.value.findIndex((a) => a.agent_id === agent.agent_id)
    if (i >= 0) fleet.value[i] = { ...fleet.value[i], ...agent, online: true }
    const j = connected.value.findIndex((a) => a.agent_id === agent.agent_id)
    if (j >= 0) connected.value[j] = { ...connected.value[j], ...agent, online: true }
    else connected.value = [...connected.value, { ...agent, online: true }]
  }

  function patchOffline(agentId: string): void {
    const i = fleet.value.findIndex((a) => a.agent_id === agentId)
    if (i >= 0) fleet.value[i] = { ...fleet.value[i]!, online: false }
    connected.value = connected.value.filter((a) => a.agent_id !== agentId)
  }

  return { fleet, total, params, filter, connected, currentVersion, loading, error, listFleet, countFleet, listConnected, refresh, mintEnrollToken, revoke, upgrade, upgradeAllOutdated, cancelUpgrade, releases, policy, loadPolicy, savePolicy, resumePolicy, autoEnroll, autoKeyParams, loadAutoEnroll, setAutoEnroll, createAutoKey, updateAutoKey, rotateAutoKey, deleteAutoKey, patchOnline, patchOffline }
})
