import { z } from 'zod'
import { tagMap } from '@freya/ui/forms'

/** Tag filter: "key" or "key=value". */
export const tagFilter = z.string().trim().max(200).regex(/^[^=\s]+(=[^=]*)?$/, 'Use key or key=value.').optional()

/** Host tag map (PATCH /hosts/{id}/tags). */
export const hostTagsSchema = z.object({ tags: tagMap(64) })
export type HostTagsInput = z.output<typeof hostTagsSchema>

export const HOST_STATUSES = ['active', 'stale', 'retired'] as const
export const LAST_SEEN_WINDOWS = ['24h', '7d', '30d'] as const

/** Host list filter form (query-string). */
export const hostFilterSchema = z.object({
  hostname: z.string().trim().max(200).optional(),
  os: z.string().trim().max(100).optional(),
  manufacturer: z.string().trim().max(100).optional(),
  status: z.enum(HOST_STATUSES).optional(),
  tag: tagFilter,
  last_seen: z.enum(LAST_SEEN_WINDOWS).optional(),
})
export type HostFilterInput = z.output<typeof hostFilterSchema>
