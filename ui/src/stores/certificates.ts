import { defineStore } from 'pinia'
import type { ListQueryOptions } from '@go-tangra/ui'
import { api } from '@/api/client'
import type { components } from '@/api/schema.d'
import type { CertificateDeliveryItem, DeliveryState, HostCertificate, ListParams, Page } from '@/api/types'

// Certificates delivered to hosts (feature 033): a host's current
// certificates and the delivery history. Identity only (serial, fingerprint,
// common name, expiry); the API never returns material.

export const PAGE_SIZE = 25

/** Sortable fields of GET /hosts/{id}/certificates (server Spec store.HostCertList). */
export const HOST_CERT_LIST: ListQueryOptions = { sortable: ['name', 'state', 'not_after', 'last_delivered_at'], defaultSort: { key: 'name', dir: 'asc' }, defaultSize: PAGE_SIZE }
/** Sortable fields of GET /certificate-deliveries (server Spec store.CertItemList). */
export const DELIVERY_LIST: ListQueryOptions = { sortable: ['created_at', 'updated_at', 'state', 'name'], defaultSort: { key: 'created_at', dir: 'desc' }, defaultSize: PAGE_SIZE }
const HOST_CERT_FIRST: ListParams = { page: 1, page_size: PAGE_SIZE, sort: 'name', order: 'asc' }
const DELIVERY_FIRST: ListParams = { page: 1, page_size: PAGE_SIZE, sort: 'created_at', order: 'desc' }

/** States in which a delivery still waits for the agent (cancellable). */
export const ACTIVE_STATES: ReadonlySet<DeliveryState> = new Set<DeliveryState>(['pending', 'delivered', 'fetched'])

export interface HostCertFilter {
  /** true: only revoked certificates; false: only the others. */
  revoked?: boolean | undefined
  state?: DeliveryState | undefined
}

export interface DeliveryFilter {
  host_id?: string | undefined
  state?: DeliveryState | undefined
  name?: string | undefined
}

function compact(f: object): Record<string, string> {
  const q: Record<string, string> = {}
  for (const [k, v] of Object.entries(f)) if (v !== undefined && v !== '') q[k] = String(v)
  return q
}

const normalize = <T>(res: Page<T>): Page<T> => ({ ...res, items: res.items ?? [], total: res.total ?? res.items?.length ?? 0 })

export const useCertificates = defineStore('inventory-certificates', () => {
  /** One page of a host's current certificates (one row per name). */
  async function forHost(hostId: string, q: ListParams = HOST_CERT_FIRST, f: HostCertFilter = {}): Promise<Page<HostCertificate>> {
    return normalize(await api<Page<HostCertificate>>('GET', 'hosts/' + hostId + '/certificates', undefined, { query: { ...compact(f), ...q } }))
  }

  /** One page of delivery items (newest first by default). */
  async function deliveries(q: ListParams = DELIVERY_FIRST, f: DeliveryFilter = {}): Promise<Page<CertificateDeliveryItem>> {
    return normalize(await api<Page<CertificateDeliveryItem>>('GET', 'certificate-deliveries', undefined, { query: { ...compact(f), ...q } }))
  }

  /** Cancels a queued delivery (agents:manage); resolves with the cancelled item. */
  async function cancel(itemId: string): Promise<CertificateDeliveryItem> {
    return api<CertificateDeliveryItem>('POST', 'certificate-deliveries/' + itemId + '/cancel')
  }

  return { forHost, deliveries, cancel }
})

// The hand-written types stay assignable to the generated OpenAPI schemas.
type Schemas = components['schemas']
type Assignable<A, B> = A extends B ? true : false
export const typesMatchSchema: [Assignable<HostCertificate, Schemas['HostCertificate']>, Assignable<CertificateDeliveryItem, Schemas['CertificateDeliveryItem']>] = [true, true]
