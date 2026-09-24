import { defineStore } from 'pinia'
import { ref } from 'vue'
import { useHosts } from '@/stores/hosts'
import { useAgents } from '@/stores/agents'
import type { ConnectedAgent } from '@/api/types'

// A single shared EventSource relays the module's live events through the
// gateway. snapshot.received bumps the host's last_seen in place; agent.online /
// agent.offline patch the connected-agents registry. The stream is
// reference-counted so several views share one connection.
export type Listener = (type: string, data: unknown) => void

const EVENTS = ['snapshot.received', 'agent.online', 'agent.offline']

interface SnapshotReceived {
  host_id: string
  snapshot_id: string
  hostname?: string
  collected_at?: string
}

interface AgentEvent {
  agent_id: string
  host_id?: string
  hostname?: string
  version?: string
  connected_at?: string
}

export const useLive = defineStore('inventory-live', () => {
  const connected = ref(false)
  let source: EventSource | null = null
  let refs = 0
  const listeners = new Set<Listener>()

  function handle(type: string, raw: string): void {
    let data: unknown = {}
    try {
      data = JSON.parse(raw)
    } catch {
      /* non-JSON payloads are ignored */
    }
    if (type === 'snapshot.received') {
      const ev = data as SnapshotReceived
      if (ev && ev.host_id) useHosts().patchLastSeen(ev.host_id, ev.collected_at ?? new Date().toISOString(), ev.snapshot_id)
    } else if (type === 'agent.online') {
      const ev = data as AgentEvent
      if (ev && ev.agent_id) useAgents().patchOnline(ev as ConnectedAgent)
    } else if (type === 'agent.offline') {
      const ev = data as AgentEvent
      if (ev && ev.agent_id) useAgents().patchOffline(ev.agent_id)
    }
    for (const l of listeners) l(type, data)
  }

  function open(): void {
    if (source) return
    source = new EventSource('/api/inventory/v1/stream', { withCredentials: true })
    source.onopen = () => (connected.value = true)
    source.onerror = () => (connected.value = false)
    for (const t of EVENTS) source.addEventListener(t, (e) => handle(t, (e as MessageEvent).data))
    source.addEventListener('message', (e) => handle((e as MessageEvent).type, (e as MessageEvent).data))
  }

  /** Opens the stream (first caller) and returns a release function. */
  function connect(): () => void {
    refs += 1
    open()
    return () => {
      refs -= 1
      if (refs <= 0) close()
    }
  }

  function close(): void {
    refs = 0
    source?.close()
    source = null
    connected.value = false
  }

  function on(l: Listener): () => void {
    listeners.add(l)
    return () => listeners.delete(l)
  }

  // Exposed for tests: inject a fake event.
  function _emit(type: string, raw: string): void {
    handle(type, raw)
  }

  return { connected, connect, close, on, _emit }
})
