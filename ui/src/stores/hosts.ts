import { defineStore } from 'pinia'
import { ref } from 'vue'
import type { ListQueryOptions } from '@go-tangra/ui'
import { api } from '@/api/client'
import type { Host, ListParams, Page, Snapshot } from '@/api/types'

export const PAGE_SIZE = 25

/** Sortable fields of GET /hosts (server Spec store.HostList). */
export const HOST_SORTS = ['hostname', 'os_name', 'manufacturer', 'status', 'last_seen', 'created_at'] as const
export const HOST_LIST: ListQueryOptions = { sortable: [...HOST_SORTS], defaultSort: { key: 'hostname', dir: 'asc' }, defaultSize: PAGE_SIZE }
const FIRST_PAGE: ListParams = { page: 1, page_size: PAGE_SIZE, sort: 'hostname', order: 'asc' }

/** GET /hosts filters (blank values are not sent). */
export interface HostFilter {
  hostname?: string | undefined
  /** Exact OS name. */
  os_name?: string | undefined
  manufacturer?: string | undefined
  status?: string | undefined
  tag?: string | undefined
  last_seen_from?: string | undefined
  last_seen_to?: string | undefined
}

export const useHosts = defineStore('inventory-hosts', () => {
  const items = ref<Host[]>([])
  const total = ref(0)
  const params = ref<ListParams>({ ...FIRST_PAGE })
  const filter = ref<HostFilter>({})
  const loading = ref(false)
  const error = ref('')
  let seq = 0

  /**
   * Loads one server page of hosts with the filter. Resolves with the page,
   * or null when it failed or a newer request superseded it (its rows are
   * then ignored).
   */
  async function list(f: HostFilter = filter.value, q: ListParams = params.value): Promise<Page<Host> | null> {
    const mine = ++seq
    loading.value = true
    error.value = ''
    filter.value = { ...f }
    params.value = { ...q }
    try {
      const res = await api<Page<Host>>('GET', 'hosts', undefined, { query: { ...f, ...q } })
      if (mine !== seq) return null
      items.value = res.items ?? []
      total.value = res.total ?? items.value.length
      return res
    } catch (e) {
      if (mine === seq) error.value = (e as Error).message
      return null
    } finally {
      if (mine === seq) loading.value = false
    }
  }

  /** Reloads the current page with the current filter and order. */
  const reload = () => list()

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
    if (total.value > 0) total.value--
  }

  // patchLastSeen bumps a host's last_seen / last_snapshot_id in place from a
  // live snapshot.received event (no refetch).
  function patchLastSeen(id: string, lastSeen: string, snapshotId?: string): void {
    const i = items.value.findIndex((x) => x.id === id)
    if (i < 0) return
    const cur = items.value[i]
    if (cur) items.value[i] = { ...cur, last_seen: lastSeen, ...(snapshotId ? { last_snapshot_id: snapshotId } : {}) }
  }

  return { items, total, params, filter, loading, error, list, reload, get, latest, setTags, retire, remove, patchLastSeen }
})
