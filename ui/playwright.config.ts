import { defineConfig } from '@playwright/test'

// Runs through the platform gateway (the remote only exists inside the shell).
// PW_CHANNEL=chrome uses the installed Google Chrome instead of the bundled Chromium.
export default defineConfig({
  testDir: 'tests/e2e',
  timeout: 90_000,
  use: {
    baseURL: process.env.E2E_BASE ?? 'https://localhost:8443',
    ignoreHTTPSErrors: true,
    testIdAttribute: 'data-test',
    ...(process.env.PW_CHANNEL ? { channel: process.env.PW_CHANNEL } : {}),
  },
  reporter: [['list'], ['html', { open: 'never' }]],
})
