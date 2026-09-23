// jsdom lacks a few browser APIs the kit touches on mount.
import { expect } from 'vitest'
import * as axeMatchers from 'vitest-axe/matchers'
expect.extend(axeMatchers)

class RO {
  observe(): void {}
  unobserve(): void {}
  disconnect(): void {}
}
globalThis.ResizeObserver = RO as unknown as typeof ResizeObserver
;(globalThis as unknown as { __vw: number }).__vw ??= 1280
window.matchMedia = (query: string): MediaQueryList => {
  const m = /min-width:\s*(\d+)px/.exec(query)
  const matches = m ? (globalThis as unknown as { __vw: number }).__vw >= Number(m[1]) : false
  return { matches, media: query, onchange: null, addListener() {}, removeListener() {}, addEventListener() {}, removeEventListener() {}, dispatchEvent: () => false } as MediaQueryList
}
