<script setup lang="ts">
import { computed, onMounted, onUnmounted, ref } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import { useHosts } from '@/stores/hosts'
import { useAgents } from '@/stores/agents'
import { useSnapshots } from '@/stores/snapshots'
import { useLive } from '@/stores/live'
import { describe } from '@/api/client'
import KeyValueTable from '@/components/KeyValueTable.vue'
import type { Change, Host, Snapshot, SnapshotDiff } from '@/api/types'

const route = useRoute()
const router = useRouter()
const hosts = useHosts()
const agents = useAgents()
const snapshots = useSnapshots()
const live = useLive()

const id = computed(() => String(route.params.id))
const host = ref<Host | null>(null)
const latest = ref<Snapshot | null>(null)
const changes = ref<Change[]>([])
const error = ref('')
const tab = ref('hardware')
const busy = ref(false)

// diff selection
const diffA = ref<string | null>(null)
const diffB = ref<string | null>(null)
const diff = ref<SnapshotDiff | null>(null)
const diffError = ref('')

let release: (() => void) | null = null

onMounted(() => {
  void load()
  void agents.listConnected()
  release = live.connect()
})
onUnmounted(() => release?.())

async function load(): Promise<void> {
  error.value = ''
  try {
    host.value = await hosts.get(id.value)
  } catch (e) {
    error.value = describe(e)
    return
  }
  try {
    latest.value = await hosts.latest(id.value)
  } catch {
    latest.value = null
  }
  void snapshots.listForHost(id.value)
  try {
    changes.value = await snapshots.changes(id.value)
  } catch {
    changes.value = []
  }
}

const online = computed(() => agents.connected.some((a) => a.host_id === id.value))

const statusColor: Record<string, string> = { active: 'success', stale: 'warning', retired: 'grey' }

const inv = computed(() => latest.value?.payload ?? null)

function humanBytes(bytes?: number): string {
  if (!bytes) return '—'
  const units = ['B', 'KB', 'MB', 'GB', 'TB', 'PB']
  const i = Math.floor(Math.log(bytes) / Math.log(1024))
  return (bytes / Math.pow(1024, i)).toFixed(i ? 1 : 0) + ' ' + units[i]
}

function fmt(ts?: string): string {
  return ts ? new Date(ts).toLocaleString() : '—'
}

async function retire(): Promise<void> {
  busy.value = true
  try {
    host.value = await hosts.retire(id.value)
  } catch (e) {
    error.value = describe(e)
  } finally {
    busy.value = false
  }
}

async function refresh(): Promise<void> {
  busy.value = true
  error.value = ''
  try {
    await agents.refresh(id.value)
  } catch (e) {
    error.value = describe(e)
  } finally {
    busy.value = false
  }
}

async function remove(): Promise<void> {
  busy.value = true
  try {
    await hosts.remove(id.value)
    void router.push({ name: 'inventory-hosts' })
  } catch (e) {
    error.value = describe(e)
    busy.value = false
  }
}

const snapshotOptions = computed(() => snapshots.items.map((s) => ({ title: fmt(s.collected_at) + ' · ' + s.id.slice(0, 8), value: s.id })))

async function compare(): Promise<void> {
  diffError.value = ''
  diff.value = null
  if (!diffA.value || !diffB.value) {
    diffError.value = 'Pick two snapshots to compare.'
    return
  }
  try {
    diff.value = await snapshots.diff(diffA.value, diffB.value)
  } catch (e) {
    diffError.value = describe(e)
  }
}

const changeColor: Record<string, string> = { added: 'success', removed: 'error', modified: 'warning' }

function groupedDiff(type: string): Change[] {
  return (diff.value?.changes ?? []).filter((c) => c.change_type === type)
}
</script>

<template>
  <div>
    <div class="d-flex align-center mb-2">
      <v-btn variant="text" icon="mdi-arrow-left" @click="router.push({ name: 'inventory-hosts' })" />
      <h1 class="text-h5 ms-2">{{ host?.hostname ?? id }}</h1>
      <v-chip v-if="host" size="small" :color="statusColor[host.status]" variant="flat" class="ms-3">{{ host.status }}</v-chip>
      <v-chip v-if="online" size="small" color="success" variant="tonal" class="ms-2" prepend-icon="mdi-circle">agent online</v-chip>
      <v-chip v-if="live.connected" size="x-small" color="success" variant="tonal" class="ms-2">live</v-chip>
      <v-spacer />
      <v-btn size="small" variant="tonal" prepend-icon="mdi-refresh" :loading="busy" class="me-2" @click="refresh">Refresh</v-btn>
      <v-btn v-if="host && host.status !== 'retired'" size="small" variant="tonal" color="warning" class="me-2" :loading="busy" @click="retire">Retire</v-btn>
      <v-btn size="small" variant="tonal" color="error" :loading="busy" @click="remove">Delete</v-btn>
    </div>

    <v-alert v-if="error" type="error" variant="tonal" density="compact" class="mb-3">{{ error }}</v-alert>

    <v-card v-if="host" variant="tonal" class="mb-4">
      <v-card-text>
        <v-row dense>
          <v-col cols="6" md="3"><div class="text-caption text-medium-emphasis">Manufacturer</div><div>{{ host.manufacturer || '—' }}</div></v-col>
          <v-col cols="6" md="3"><div class="text-caption text-medium-emphasis">Model</div><div>{{ host.model || '—' }}</div></v-col>
          <v-col cols="6" md="3"><div class="text-caption text-medium-emphasis">OS</div><div>{{ host.os_name }} {{ host.os_version }} {{ host.os_arch }}</div></v-col>
          <v-col cols="6" md="3"><div class="text-caption text-medium-emphasis">Agent</div><div>{{ host.agent_version || '—' }}</div></v-col>
          <v-col cols="6" md="3"><div class="text-caption text-medium-emphasis">Assigned user</div><div>{{ host.assigned_user || '—' }}</div></v-col>
          <v-col cols="6" md="3"><div class="text-caption text-medium-emphasis">Serial</div><div>{{ host.system_serial || '—' }}</div></v-col>
          <v-col cols="6" md="3"><div class="text-caption text-medium-emphasis">First seen</div><div>{{ fmt(host.first_seen) }}</div></v-col>
          <v-col cols="6" md="3"><div class="text-caption text-medium-emphasis">Last seen</div><div>{{ fmt(host.last_seen) }}</div></v-col>
        </v-row>
        <div v-if="host.tags && Object.keys(host.tags).length" class="mt-3">
          <v-chip v-for="(v, k) in host.tags" :key="k" size="x-small" variant="tonal" class="me-1 mb-1">{{ k }}: {{ v }}</v-chip>
        </div>
      </v-card-text>
    </v-card>

    <v-tabs v-model="tab" class="mb-4">
      <v-tab value="hardware">Hardware</v-tab>
      <v-tab value="software">Software</v-tab>
      <v-tab value="network">Network</v-tab>
      <v-tab value="history">History</v-tab>
    </v-tabs>

    <v-alert v-if="tab !== 'history' && !inv" type="info" variant="tonal" density="compact">No snapshot collected yet.</v-alert>

    <v-window v-model="tab">
      <!-- Hardware -->
      <v-window-item value="hardware">
        <template v-if="inv">
          <v-row>
            <v-col cols="12" md="6">
              <KeyValueTable
                title="BIOS"
                :rows="[['Vendor', inv.bios.vendor], ['Version', inv.bios.version], ['Release date', inv.bios.release_date]]"
              />
            </v-col>
            <v-col cols="12" md="6">
              <KeyValueTable
                title="System"
                :rows="[['Manufacturer', inv.system.manufacturer], ['Product', inv.system.product_name], ['Version', inv.system.version], ['Serial', inv.system.serial_number], ['Family', inv.system.family], ['SKU', inv.system.sku_number]]"
              />
            </v-col>
            <v-col cols="12" md="6">
              <KeyValueTable
                title="Baseboard"
                :rows="[['Manufacturer', inv.baseboard.manufacturer], ['Product', inv.baseboard.product], ['Version', inv.baseboard.version], ['Serial', inv.baseboard.serial_number], ['Asset tag', inv.baseboard.asset_tag]]"
              />
            </v-col>
            <v-col cols="12" md="6">
              <KeyValueTable
                title="Chassis"
                :rows="[['Manufacturer', inv.chassis.manufacturer], ['Type', inv.chassis.type], ['Version', inv.chassis.version], ['Serial', inv.chassis.serial_number], ['Asset tag', inv.chassis.asset_tag]]"
              />
            </v-col>
          </v-row>

          <v-card variant="tonal" class="mb-4">
            <v-card-title class="text-subtitle-1">Processors</v-card-title>
            <v-card-text class="pt-0">
              <v-table density="compact">
                <thead><tr><th>Socket</th><th>Manufacturer</th><th>Version</th><th>Cores</th><th>Threads</th><th>Max MHz</th></tr></thead>
                <tbody>
                  <tr v-for="(p, i) in inv.processors ?? []" :key="i">
                    <td>{{ p.socket_designation || '—' }}</td>
                    <td>{{ p.manufacturer || '—' }}</td>
                    <td class="text-medium-emphasis">{{ p.version || '—' }}</td>
                    <td>{{ p.core_count || '—' }}</td>
                    <td>{{ p.thread_count || '—' }}</td>
                    <td>{{ p.max_speed_mhz || '—' }}</td>
                  </tr>
                  <tr v-if="!(inv.processors ?? []).length"><td colspan="6" class="text-medium-emphasis">Not collected.</td></tr>
                </tbody>
              </v-table>
            </v-card-text>
          </v-card>

          <v-card variant="tonal" class="mb-4">
            <v-card-title class="text-subtitle-1">
              Memory
              <span class="text-body-2 text-medium-emphasis ms-2">total {{ humanBytes(inv.memory.total_physical_bytes) }}</span>
            </v-card-title>
            <v-card-text class="pt-0">
              <v-table density="compact">
                <thead><tr><th>Locator</th><th>Capacity</th><th>Type</th><th>Form factor</th><th>Speed MT/s</th><th>Manufacturer</th></tr></thead>
                <tbody>
                  <tr v-for="(m, i) in inv.memory.modules ?? []" :key="i">
                    <td>{{ m.device_locator || '—' }}</td>
                    <td>{{ humanBytes(m.capacity_bytes) }}</td>
                    <td>{{ m.memory_type || '—' }}</td>
                    <td class="text-medium-emphasis">{{ m.form_factor || '—' }}</td>
                    <td>{{ m.speed_mt_s || '—' }}</td>
                    <td class="text-medium-emphasis">{{ m.manufacturer || '—' }}</td>
                  </tr>
                  <tr v-if="!(inv.memory.modules ?? []).length"><td colspan="6" class="text-medium-emphasis">Not collected.</td></tr>
                </tbody>
              </v-table>
            </v-card-text>
          </v-card>

          <v-card variant="tonal" class="mb-4">
            <v-card-title class="text-subtitle-1">Disks</v-card-title>
            <v-card-text class="pt-0">
              <div v-for="(d, i) in inv.disks ?? []" :key="i" class="mb-3">
                <div class="d-flex align-center mb-1">
                  <span class="font-weight-medium">{{ d.model || 'Disk ' + (i + 1) }}</span>
                  <v-chip size="x-small" variant="tonal" class="ms-2">{{ humanBytes(d.size_bytes) }}</v-chip>
                  <v-chip v-if="d.media_type" size="x-small" variant="tonal" class="ms-1">{{ d.media_type }}</v-chip>
                  <v-chip v-if="d.interface" size="x-small" variant="tonal" class="ms-1">{{ d.interface }}</v-chip>
                </div>
                <v-table density="compact">
                  <thead><tr><th>Mount</th><th>FS</th><th>Size</th><th>Free</th></tr></thead>
                  <tbody>
                    <tr v-for="(p, j) in d.partitions ?? []" :key="j">
                      <td>{{ p.mount || '—' }}</td>
                      <td class="text-medium-emphasis">{{ p.fs || '—' }}</td>
                      <td>{{ humanBytes(p.size_bytes) }}</td>
                      <td>{{ humanBytes(p.free_bytes) }}</td>
                    </tr>
                    <tr v-if="!(d.partitions ?? []).length"><td colspan="4" class="text-medium-emphasis">No partitions.</td></tr>
                  </tbody>
                </v-table>
              </div>
              <div v-if="!(inv.disks ?? []).length" class="text-medium-emphasis">Not collected.</div>
            </v-card-text>
          </v-card>

          <v-card variant="tonal" class="mb-4">
            <v-card-title class="text-subtitle-1">Monitors</v-card-title>
            <v-card-text class="pt-0">
              <v-table density="compact">
                <thead><tr><th>Manufacturer</th><th>Model</th><th>Serial</th></tr></thead>
                <tbody>
                  <tr v-for="(m, i) in inv.monitors ?? []" :key="i">
                    <td>{{ m.manufacturer || '—' }}</td>
                    <td>{{ m.model || '—' }}</td>
                    <td class="text-medium-emphasis">{{ m.serial_number || '—' }}</td>
                  </tr>
                  <tr v-if="!(inv.monitors ?? []).length"><td colspan="3" class="text-medium-emphasis">Not collected.</td></tr>
                </tbody>
              </v-table>
            </v-card-text>
          </v-card>
        </template>
      </v-window-item>

      <!-- Software -->
      <v-window-item value="software">
        <template v-if="inv">
          <KeyValueTable
            title="Operating system"
            :rows="[['Name', inv.os.name], ['Version', inv.os.version], ['Build', inv.os.build], ['Arch', inv.os.arch], ['Kernel', inv.os.kernel], ['Install date', fmt(inv.os.install_date)], ['Last boot', fmt(inv.os.last_boot)]]"
          />
          <KeyValueTable
            title="Environment"
            :rows="[['Domain', inv.environment.domain], ['Workgroup', inv.environment.workgroup], ['Timezone', inv.environment.timezone], ['Locale', inv.environment.locale]]"
          />

          <v-card variant="tonal" class="mb-4">
            <v-card-title class="text-subtitle-1">Installed programs <span class="text-body-2 text-medium-emphasis ms-2">{{ (inv.installed_programs ?? []).length }}</span></v-card-title>
            <v-card-text class="pt-0">
              <v-table density="compact" fixed-header height="340">
                <thead><tr><th>Name</th><th>Version</th><th>Publisher</th><th>Installed</th></tr></thead>
                <tbody>
                  <tr v-for="(p, i) in inv.installed_programs ?? []" :key="i">
                    <td>{{ p.name }}</td>
                    <td class="text-medium-emphasis">{{ p.version || '—' }}</td>
                    <td class="text-medium-emphasis">{{ p.publisher || '—' }}</td>
                    <td class="text-medium-emphasis">{{ p.install_date || '—' }}</td>
                  </tr>
                  <tr v-if="!(inv.installed_programs ?? []).length"><td colspan="4" class="text-medium-emphasis">Not collected.</td></tr>
                </tbody>
              </v-table>
            </v-card-text>
          </v-card>

          <v-card variant="tonal" class="mb-4">
            <v-card-title class="text-subtitle-1">Services <span class="text-body-2 text-medium-emphasis ms-2">{{ (inv.services ?? []).length }}</span></v-card-title>
            <v-card-text class="pt-0">
              <v-table density="compact" fixed-header height="340">
                <thead><tr><th>Name</th><th>Display name</th><th>State</th><th>Start mode</th></tr></thead>
                <tbody>
                  <tr v-for="(s, i) in inv.services ?? []" :key="i">
                    <td>{{ s.name }}</td>
                    <td class="text-medium-emphasis">{{ s.display_name || '—' }}</td>
                    <td><v-chip size="x-small" :color="s.state === 'running' ? 'success' : 'grey'" variant="tonal">{{ s.state || '—' }}</v-chip></td>
                    <td class="text-medium-emphasis">{{ s.start_mode || '—' }}</td>
                  </tr>
                  <tr v-if="!(inv.services ?? []).length"><td colspan="4" class="text-medium-emphasis">Not collected.</td></tr>
                </tbody>
              </v-table>
            </v-card-text>
          </v-card>

          <v-row>
            <v-col cols="12" md="6">
              <v-card variant="tonal" class="mb-4">
                <v-card-title class="text-subtitle-1">Users</v-card-title>
                <v-card-text class="pt-0">
                  <v-table density="compact">
                    <thead><tr><th>Name</th><th>Admin</th><th>Last logon</th></tr></thead>
                    <tbody>
                      <tr v-for="(u, i) in inv.users ?? []" :key="i">
                        <td>{{ u.name }}</td>
                        <td><v-icon v-if="u.is_admin" size="small" icon="mdi-shield-account" color="warning" /><span v-else class="text-medium-emphasis">—</span></td>
                        <td class="text-medium-emphasis">{{ fmt(u.last_logon) }}</td>
                      </tr>
                      <tr v-if="!(inv.users ?? []).length"><td colspan="3" class="text-medium-emphasis">Not collected.</td></tr>
                    </tbody>
                  </v-table>
                </v-card-text>
              </v-card>
            </v-col>
            <v-col cols="12" md="6">
              <v-card variant="tonal" class="mb-4">
                <v-card-title class="text-subtitle-1">Patches</v-card-title>
                <v-card-text class="pt-0">
                  <v-table density="compact">
                    <thead><tr><th>ID</th><th>Installed on</th></tr></thead>
                    <tbody>
                      <tr v-for="(p, i) in inv.patches ?? []" :key="i">
                        <td>{{ p.id }}</td>
                        <td class="text-medium-emphasis">{{ p.installed_on || '—' }}</td>
                      </tr>
                      <tr v-if="!(inv.patches ?? []).length"><td colspan="2" class="text-medium-emphasis">Not collected.</td></tr>
                    </tbody>
                  </v-table>
                </v-card-text>
              </v-card>
            </v-col>
          </v-row>
        </template>
      </v-window-item>

      <!-- Network -->
      <v-window-item value="network">
        <template v-if="inv">
          <v-card variant="tonal" class="mb-4">
            <v-card-title class="text-subtitle-1">Network interfaces</v-card-title>
            <v-card-text class="pt-0">
              <v-table density="compact">
                <thead><tr><th>Name</th><th>MAC</th><th>Addresses</th><th>Gateway</th><th>DNS</th><th>Type</th><th>State</th></tr></thead>
                <tbody>
                  <tr v-for="(n, i) in inv.network_interfaces ?? []" :key="i">
                    <td>{{ n.name }}</td>
                    <td class="text-medium-emphasis">{{ n.mac || '—' }}</td>
                    <td>{{ (n.ip_addresses ?? []).join(', ') || '—' }}</td>
                    <td class="text-medium-emphasis">{{ n.gateway || '—' }}</td>
                    <td class="text-medium-emphasis">{{ (n.dns ?? []).join(', ') || '—' }}</td>
                    <td class="text-medium-emphasis">{{ n.type || '—' }}</td>
                    <td><v-chip size="x-small" :color="n.up ? 'success' : 'grey'" variant="tonal">{{ n.up ? 'up' : 'down' }}</v-chip></td>
                  </tr>
                  <tr v-if="!(inv.network_interfaces ?? []).length"><td colspan="7" class="text-medium-emphasis">Not collected.</td></tr>
                </tbody>
              </v-table>
            </v-card-text>
          </v-card>
        </template>
      </v-window-item>

      <!-- History -->
      <v-window-item value="history">
        <v-card variant="tonal" class="mb-4">
          <v-card-title class="text-subtitle-1">Compare snapshots</v-card-title>
          <v-card-text>
            <v-row dense align="center">
              <v-col cols="12" md="5"><v-select v-model="diffA" :items="snapshotOptions" label="From" density="compact" hide-details /></v-col>
              <v-col cols="12" md="5"><v-select v-model="diffB" :items="snapshotOptions" label="To" density="compact" hide-details /></v-col>
              <v-col cols="12" md="2"><v-btn color="primary" block :disabled="!diffA || !diffB" data-test="diff-compare" @click="compare">Compare</v-btn></v-col>
            </v-row>
            <v-alert v-if="diffError" type="error" variant="tonal" density="compact" class="mt-3">{{ diffError }}</v-alert>
            <div v-if="diff" class="mt-4">
              <div v-for="type in ['added', 'removed', 'modified']" :key="type" class="mb-3">
                <div class="d-flex align-center mb-1">
                  <v-chip size="x-small" :color="changeColor[type]" variant="flat" class="me-2">{{ type }}</v-chip>
                  <span class="text-body-2 text-medium-emphasis">{{ groupedDiff(type).length }}</span>
                </div>
                <v-table v-if="groupedDiff(type).length" density="compact">
                  <thead><tr><th>Category</th><th>Component</th><th>Before</th><th>After</th></tr></thead>
                  <tbody>
                    <tr v-for="(c, i) in groupedDiff(type)" :key="i">
                      <td>{{ c.category }}</td>
                      <td>{{ c.component_key }}</td>
                      <td class="text-medium-emphasis text-truncate" style="max-width: 220px">{{ c.before || '—' }}</td>
                      <td class="text-medium-emphasis text-truncate" style="max-width: 220px">{{ c.after || '—' }}</td>
                    </tr>
                  </tbody>
                </v-table>
              </div>
            </div>
          </v-card-text>
        </v-card>

        <v-card variant="tonal" class="mb-4">
          <v-card-title class="text-subtitle-1">Snapshots</v-card-title>
          <v-card-text class="pt-0">
            <v-table density="compact" data-test="snapshots-table">
              <thead><tr><th>Collected</th><th>Received</th><th>Source</th><th>Agent</th><th>ID</th></tr></thead>
              <tbody>
                <tr v-for="s in snapshots.items" :key="s.id">
                  <td>{{ fmt(s.collected_at) }}</td>
                  <td class="text-medium-emphasis">{{ fmt(s.received_at) }}</td>
                  <td><v-chip size="x-small" variant="tonal">{{ s.source }}</v-chip></td>
                  <td class="text-medium-emphasis">{{ s.agent_version || '—' }}</td>
                  <td class="text-medium-emphasis">{{ s.id.slice(0, 8) }}</td>
                </tr>
                <tr v-if="!snapshots.items.length"><td colspan="5" class="text-medium-emphasis">No snapshots.</td></tr>
              </tbody>
            </v-table>
          </v-card-text>
        </v-card>

        <v-card variant="tonal" class="mb-4">
          <v-card-title class="text-subtitle-1">Change history</v-card-title>
          <v-card-text class="pt-0">
            <v-table density="compact">
              <thead><tr><th>Detected</th><th>Category</th><th>Change</th><th>Component</th></tr></thead>
              <tbody>
                <tr v-for="(c, i) in changes" :key="i">
                  <td class="text-medium-emphasis">{{ fmt(c.detected_at) }}</td>
                  <td>{{ c.category }}</td>
                  <td><v-chip size="x-small" :color="changeColor[c.change_type]" variant="tonal">{{ c.change_type }}</v-chip></td>
                  <td>{{ c.component_key }}</td>
                </tr>
                <tr v-if="!changes.length"><td colspan="4" class="text-medium-emphasis">No changes recorded.</td></tr>
              </tbody>
            </v-table>
          </v-card-text>
        </v-card>
      </v-window-item>
    </v-window>
  </div>
</template>
