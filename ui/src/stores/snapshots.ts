import { defineStore } from 'pinia'
import { ref } from 'vue'
import { api } from '@/api/client'
import type { Change, Snapshot, SnapshotDiff } from '@/api/types'

export const useSnapshots = defineStore('inventory-snapshots', () => {
  const items = ref<Snapshot[]>([])
  const loading = ref(false)
  const error = ref('')

  // listForHost loads a host's snapshot history (newest-first).
  async function listForHost(hostId: string, limit = 50): Promise<void> {
    loading.value = true
    error.value = ''
    try {
      const res = await api<{ items: Snapshot[] }>('GET', 'hosts/' + hostId + '/snapshots', undefined, { query: { limit } })
      items.value = res.items ?? []
    } catch (e) {
      error.value = (e as Error).message
    } finally {
      loading.value = false
    }
  }

  async function get(id: string): Promise<Snapshot> {
    return api<Snapshot>('GET', 'snapshots/' + id)
  }

  // diff compares two snapshots → added/removed/modified changes.
  async function diff(a: string, b: string): Promise<SnapshotDiff> {
    return api<SnapshotDiff>('GET', 'snapshots/' + a + '/diff/' + b)
  }

  // changes loads the detected change history for a host.
  async function changes(hostId: string, limit = 100): Promise<Change[]> {
    const res = await api<{ items: Change[] }>('GET', 'hosts/' + hostId + '/changes', undefined, { query: { limit } })
    return res.items ?? []
  }

  async function remove(id: string): Promise<void> {
    await api('DELETE', 'snapshots/' + id)
    items.value = items.value.filter((x) => x.id !== id)
  }

  return { items, loading, error, listForHost, get, diff, changes, remove }
})
