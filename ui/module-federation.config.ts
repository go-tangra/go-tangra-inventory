// Shared singletons must match the platform shell
// (specs/003-application-gateway/contracts/federation.md).
export const shared = {
  vue: { singleton: true, requiredVersion: '^3.5.0' },
  'vue-router': { singleton: true, requiredVersion: '^5.0.0' },
  pinia: { singleton: true, requiredVersion: '^4.0.0' },
  vuetify: { singleton: true, requiredVersion: '^4.0.0' },
  '@casl/ability': { singleton: true, requiredVersion: '^7.0.0' },
  '@casl/vue': { singleton: true, requiredVersion: '^3.0.0' },
}

export const remoteConfig = {
  name: 'inventory',
  filename: 'remoteEntry.js',
  manifest: true,
  exposes: {
    './routes': './src/remote/routes.ts',
    './nav': './src/remote/nav.ts',
  },
  shared,
  // The shell loads remotes at runtime; no consumer imports generated types.
  dts: false,
}
