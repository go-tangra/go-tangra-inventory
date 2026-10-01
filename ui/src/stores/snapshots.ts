import { defineStore } from 'pinia'
import { ref } from 'vue'
import type { ListQueryOptions } from '@go-tangra/ui'
import { api } from '@/api/client'
import type { Change, ListParams, Page, Snapshot, SnapshotDiff } from '@/api/types'

export const PAGE_SIZE = 25

/** Sortable fields of GET /hosts/{id}/snapshots (server Spec store.SnapshotList). */
export const SNAPSHOT_LIST: ListQueryOptions = { sortable: ['collected_at'], defaultSort: { key: 'collected_at', dir: 'desc' }, defaultSize: PAGE_SIZE }
/** Sortable fields of GET /hosts/{id}/changes (server Spec store.ChangeList; kind is the change type). */
export const CHANGE_LIST: ListQueryOptions = { sortable: ['detected_at', 'kind'], defaultSort: { key: 'detected_at', dir: 'desc' }, defaultSize: PAGE_SIZE }
const SNAPSHOT_FIRST: ListParams = { page: 1, page_size: PAGE_SIZE, sort: 'collected_at', order: 'desc' }
const CHANGE_FIRST: ListParams = { page: 1, page_size: PAGE_SIZE, sort: 'detected_at', order: 'desc' }

export const useSnapshots = defineStore('inventory-snapshots', () => {
  // One server page of a host's snapshot summaries (no payload).
  const items = ref<Snapshot[]>([])
  const total = ref(0)
  const loading = ref(false)
  const error = ref('')
  let seq = 0

  /**
   * Loads one page of a host's snapshot history (newest first by default).
   * Resolves with the page, or null when it failed or was superseded.
   */
  async function listForHost(hostId: string, q: ListParams = SNAPSHOT_FIRST): Promise<Page<Snapshot> | null> {
    const mine = ++seq
    loading.value = true
    error.value = ''
    try {
      const res = await api<Page<Snapshot>>('GET', 'hosts/' + hostId + '/snapshots', undefined, { query: { ...q } })
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

  async function get(id: string): Promise<Snapshot> {
    return api<Snapshot>('GET', 'snapshots/' + id)
  }

  // diff compares two snapshots → added/removed/modified changes.
  async function diff(a: string, b: string): Promise<SnapshotDiff> {
    return api<SnapshotDiff>('GET', 'snapshots/' + a + '/diff/' + b)
  }

  /** One page of a host's detected change history (newest first by default). */
  async function changes(hostId: string, q: ListParams = CHANGE_FIRST): Promise<Page<Change>> {
    const res = await api<Page<Change>>('GET', 'hosts/' + hostId + '/changes', undefined, { query: { ...q } })
    return { ...res, items: res.items ?? [], total: res.total ?? res.items?.length ?? 0 }
  }

  async function remove(id: string): Promise<void> {
    await api('DELETE', 'snapshots/' + id)
    items.value = items.value.filter((x) => x.id !== id)
    if (total.value > 0) total.value--
  }

  return { items, total, loading, error, listForHost, get, diff, changes, remove }
})
