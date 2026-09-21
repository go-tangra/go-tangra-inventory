import { defineStore } from 'pinia'
import { ref } from 'vue'
import { api } from '@/api/client'
import type { Host, Snapshot } from '@/api/types'

export interface HostFilter {
  hostname?: string | undefined
  os?: string | undefined
  manufacturer?: string | undefined
  status?: string | undefined
  tag?: string | undefined
  last_seen_from?: string | undefined
  last_seen_to?: string | undefined
  agent_online?: boolean | undefined
  cursor?: string | undefined
  limit?: number | undefined
}

export const useHosts = defineStore('inventory-hosts', () => {
  const items = ref<Host[]>([])
  const loading = ref(false)
  const error = ref('')

  async function list(filter: HostFilter = {}): Promise<void> {
    loading.value = true
    error.value = ''
    try {
      const res = await api<{ items: Host[] }>('GET', 'hosts', undefined, { query: { ...filter } })
      items.value = res.items ?? []
    } catch (e) {
      error.value = (e as Error).message
    } finally {
      loading.value = false
    }
  }

  // get returns the host with its latest snapshot summary.
  async function get(id: string): Promise<Host> {
    return api<Host>('GET', 'hosts/' + id)
  }

  // latest returns the newest full snapshot for a host.
  async function latest(id: string): Promise<Snapshot> {
    return api<Snapshot>('GET', 'hosts/' + id + '/latest')
  }

  async function setTags(id: string, tags: Record<string, string>): Promise<Host> {
    const h = await api<Host>('POST', 'hosts/' + id + '/tags', { tags })
    items.value = items.value.map((x) => (x.id === id ? h : x))
    return h
  }

  async function retire(id: string): Promise<Host> {
    const h = await api<Host>('POST', 'hosts/' + id + '/retire')
    items.value = items.value.map((x) => (x.id === id ? h : x))
    return h
  }

  async function remove(id: string): Promise<void> {
    await api('DELETE', 'hosts/' + id)
    items.value = items.value.filter((x) => x.id !== id)
  }

  // patchLastSeen bumps a host's last_seen / last_snapshot_id in place from a
  // live snapshot.received event (no refetch).
  function patchLastSeen(id: string, lastSeen: string, snapshotId?: string): void {
    const i = items.value.findIndex((x) => x.id === id)
    if (i < 0) return
    const cur = items.value[i]
    if (cur) items.value[i] = { ...cur, last_seen: lastSeen, ...(snapshotId ? { last_snapshot_id: snapshotId } : {}) }
  }

  return { items, loading, error, list, get, latest, setTags, retire, remove, patchLastSeen }
})
