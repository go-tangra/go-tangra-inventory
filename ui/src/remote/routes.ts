import type { RouteRecordRaw } from 'vue-router'

// Routes mounted by the platform shell under their own error boundary.
export const routes: RouteRecordRaw[] = [
  { path: '/inventory', name: 'inventory-hosts', component: () => import('@/views/hosts/index.vue'), meta: { module: 'inventory' } },
  { path: '/inventory/host/:id', name: 'inventory-host', component: () => import('@/views/hosts/detail.vue'), meta: { module: 'inventory' } },
  { path: '/inventory/agents', name: 'inventory-agents', component: () => import('@/views/agents/index.vue'), meta: { module: 'inventory' } },
  { path: '/inventory/dashboard', name: 'inventory-dashboard', component: () => import('@/views/dashboard/index.vue'), meta: { module: 'inventory' } },
]
export default routes
