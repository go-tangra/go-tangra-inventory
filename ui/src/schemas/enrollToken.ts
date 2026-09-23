import { z } from 'zod'
import { optionalString } from '@freya/ui/forms'

/** POST /agents/enroll-token payload: an optional label for the minted token. */
export const enrollTokenSchema = z.object({
  label: optionalString(100),
})
export type EnrollTokenInput = z.output<typeof enrollTokenSchema>
