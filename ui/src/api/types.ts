// Domain types mirror the inventory OpenAPI responses (api/openapi/inventory.yaml)
// and the store models (internal/store/models.go). Credentials and sealed fields
// are never returned in listings and are omitted here. Response projections use
// optional (`?:`) fields; inputs/filters use explicit `T | undefined` to satisfy
// exactOptionalPropertyTypes.

export type HostStatus = 'active' | 'stale' | 'retired'
export type SnapshotSource = 'agent' | 'manual' | 'import'
export type ChangeType = 'added' | 'removed' | 'modified'

// --- list contract (go-tangra specs/032-server-side-tables) ---

/** The list contract fields of a page. */
export interface PageInfo {
  total: number
  /** The page returned: a page beyond the end answers the last page. */
  page?: number
  page_size?: number
  sort?: string
  order?: 'asc' | 'desc'
}

/** Page, size and order of a list request. */
export interface ListParams {
  page: number
  page_size: number
  sort: string
  order: 'asc' | 'desc'
}

/** One page of a list endpoint. */
export interface Page<T> extends PageInfo {
  items: T[]
}

// --- host ---

export interface Host {
  id: string
  tenant_id?: string
  hostname: string
  machine_id?: string
  hardware_uuid?: string
  system_serial?: string
  identity_key?: string
  manufacturer?: string
  model?: string
  os_name?: string
  os_version?: string
  os_arch?: string
  agent_version?: string
  assigned_user?: string
  status: HostStatus
  tags?: Record<string, string>
  first_seen: string
  last_seen: string
  last_snapshot_id?: string
  created_at?: string
  updated_at?: string
}

// --- hardware ---

export interface BIOSInfo {
  vendor?: string
  version?: string
  release_date?: string
}

export interface SystemInfo {
  manufacturer?: string
  product_name?: string
  version?: string
  serial_number?: string
  uuid?: string
  wake_up_type?: string
  sku_number?: string
  family?: string
}

export interface BaseboardInfo {
  manufacturer?: string
  product?: string
  version?: string
  serial_number?: string
  asset_tag?: string
  location_in_chassis?: string
  board_type?: string
}

export interface ChassisInfo {
  manufacturer?: string
  version?: string
  serial_number?: string
  asset_tag?: string
  sku_number?: string
  type?: string // DSP0134 name, e.g. "Rack Mount Chassis"
  bootup_state?: string
}

export interface Processor {
  socket_designation?: string
  manufacturer?: string
  version?: string
  max_speed_mhz?: number
  current_speed_mhz?: number
  core_count?: number
  core_enabled?: number
  thread_count?: number
  part_number?: string
  serial_number?: string
  socket_populated?: boolean
  family?: string // DSP0134 name, e.g. "Intel Xeon processor"
  type?: string // e.g. "Central Processor"
  upgrade?: string // socket, e.g. "Socket LGA4189"
}

export interface MemoryArray {
  location?: string
  use?: string
  error_correction?: string
  maximum_capacity?: number
  number_of_devices?: number
  handle?: number
}

export interface MemoryModule {
  device_locator?: string
  bank_locator?: string
  capacity_bytes?: number
  form_factor?: string
  memory_type?: string
  speed_mt_s?: number
  configured_speed_mt_s?: number
  manufacturer?: string
  serial_number?: string
  part_number?: string
  // Feature 023 (hardware_schema >= 2): every slot is listed; an empty slot
  // has populated false/absent. Older agents listed populated modules only.
  populated?: boolean
  type_detail?: string[]
  array_handle?: number
  asset_tag?: string
  rank_count?: number
}

export interface MemoryInfo {
  total_physical_bytes?: number
  array: MemoryArray
  modules?: MemoryModule[]
  arrays?: MemoryArray[]
  slots_total?: number
  slots_populated?: number
}

export interface Monitor {
  manufacturer?: string
  model?: string
  serial_number?: string
}

// --- software / OS ---

export interface OSInfo {
  name?: string
  version?: string
  build?: string
  arch?: string
  kernel?: string
  install_date?: string
  last_boot?: string
  uptime_sec?: number
  family?: string // linux | windows
}

export interface Program {
  name: string
  version?: string
  publisher?: string
  install_date?: string
  install_location?: string
  size_bytes?: number
  available_version?: string // newer version offered by the package manager
  security_update?: boolean
}

export interface Service {
  name: string
  display_name?: string
  state?: string
  start_mode?: string
  account?: string
}

export interface UserAccount {
  name: string
  is_admin?: boolean
  last_logon?: string
}

export interface Patch {
  id: string
  installed_on?: string
}

export interface Environment {
  domain?: string
  workgroup?: string
  timezone?: string
  locale?: string
}

// --- network / storage ---

export interface NetIface {
  name: string
  mac?: string
  ip_addresses?: string[]
  subnet?: string
  gateway?: string
  dns?: string[]
  dhcp?: boolean
  speed_bps?: number
  type?: string // kind: ethernet|wireless|bond|bridge|vlan|virtual|loopback|other
  up?: boolean
  addresses?: IfAddress[]
  default_route?: boolean
  master?: string
  vlan_id?: number
}

export interface IfAddress {
  address: string
  prefix_length: number
  family: 'ipv4' | 'ipv6'
  dhcp?: boolean
  temporary?: boolean
  deprecated?: boolean
  scope?: string
}

export interface Virtualization {
  role?: string // physical|vm|container|unknown
  kind?: string
  source?: string
}

// Bmc is the out-of-band controller's LAN configuration (never credentials).
export interface Bmc {
  address?: string
  prefix_length?: number
  gateway?: string
  ip_source?: string
  vlan_id?: number
  ports?: { channel: number; mac?: string; address?: string }[]
}

export interface HypervisorGuest {
  id: string
  name?: string
  kind?: string
  platform?: string
  macs?: string[]
}

export interface UpdateState {
  package_manager?: string
  status?: string // unknown|up_to_date|updates_available|unsupported|error
  reboot_required?: string // unknown|true|false
  automatic_updates?: string // unknown|true|false
  security_classified?: boolean
  checked_at?: string
  pending_count?: number
  security_count?: number
}

export interface CollectionLimits {
  interfaces?: number
  addresses?: number
  guests?: number
  packages?: number
  bmc_ports?: number
  disks?: number
  memory_slots?: number
  memory_arrays?: number
  processors?: number
  filesystems?: number
}

export interface Partition {
  mount?: string
  fs?: string
  size_bytes?: number
  free_bytes?: number
}

// Disk is a physical disk (hardware_schema >= 2); older agents reported
// mounted partitions grouped by device in `partitions` only.
export interface Disk {
  model?: string
  serial?: string
  size_bytes?: number
  media_type?: string // ssd | hdd | nvme_ssd | unknown
  interface?: string // nvme | sata | sas | scsi | usb | virtio | hyperv | xen | mmc | other
  partitions?: Partition[]
  name?: string
  removable?: boolean
  vendor?: string
}

// Filesystem is a mounted filesystem and the disks (Disk.name) it lives on.
export interface Filesystem {
  mount: string
  fs?: string
  device?: string
  size_bytes?: number
  free_bytes?: number
  disks?: string[]
}

// HardwareAvailability: ok | partial | unavailable | unsupported | unknown.
export interface HardwareAvailability {
  smbios?: string
  disks?: string
}

export interface Identity {
  hardware_uuid?: string
  machine_id?: string
  hostname?: string
}

// Inventory is the full collected payload (hardware + software/OS + network).
export interface Inventory {
  identity?: Identity
  collected_at?: string
  agent_version?: string
  os: OSInfo
  bios: BIOSInfo
  system: SystemInfo
  baseboard: BaseboardInfo
  chassis: ChassisInfo
  processors?: Processor[]
  memory: MemoryInfo
  ports?: string[]
  slots?: string[]
  oem_strings?: string[]
  bios_language?: string
  monitors?: Monitor[]
  installed_programs?: Program[]
  services?: Service[]
  users?: UserAccount[]
  patches?: Patch[]
  environment: Environment
  network_interfaces?: NetIface[]
  disks?: Disk[]
  primary_ipv4?: string
  primary_ipv6?: string
  virtualization?: Virtualization
  bmc?: Bmc
  hypervisor_guests?: HypervisorGuest[]
  update_state?: UpdateState
  truncated?: CollectionLimits
  filesystems?: Filesystem[]
  hardware_schema?: number // < 2 (or absent): legacy decoding with known errors
  hardware_availability?: HardwareAvailability
}

// Snapshot is an immutable inventory report bound to a host.
export interface Snapshot {
  id: string
  tenant_id?: string
  host_id: string
  collected_at: string
  received_at: string
  agent_version?: string
  source: SnapshotSource
  os_name?: string
  os_version?: string
  manufacturer?: string
  model?: string
  payload: Inventory
}

// --- change history ---

export interface Change {
  id?: string
  host_id?: string
  snapshot_id?: string
  prev_snapshot_id?: string
  detected_at?: string
  category: string
  change_type: ChangeType
  component_key: string
  before?: string
  after?: string
}

// SnapshotDiff is the /snapshots/{id}/diff/{other} response.
export interface SnapshotDiff {
  from: string
  to: string
  changes: Change[]
}

// --- agents / enrollment ---

// ConnectedAgent mirrors an entry from the shared connection registry.
export interface ConnectedAgent {
  agent_id: string
  host_id?: string
  hostname?: string
  version?: string
  connected_at?: string
}

// --- agent fleet and self-upgrade (feature 023) ---

export type FleetState = 'up_to_date' | 'available' | 'pending' | 'in_progress' | 'failed' | 'rolled_back' | 'manual_upgrade_required' | 'unsupported'
export type UpgradeState = 'pending' | 'delivered' | 'downloading' | 'installing' | 'succeeded' | 'failed' | 'rolled_back' | 'expired' | 'cancelled'

// AgentFleetEntry is one enrolled agent (online or not); the connected-agent keys are kept.
export interface AgentFleetEntry extends ConnectedAgent {
  tenant_id?: string
  os?: '' | 'linux' | 'windows'
  arch?: '' | 'amd64' | 'arm64'
  install_type?: '' | 'deb' | 'rpm' | 'binary'
  // Absent on the legacy connected-only listing (every entry is online there).
  online?: boolean
  last_seen?: string
  target_version?: string
  upgrade_state?: FleetState
  upgrade_reason?: string
  upgrade_id?: string
  state_changed_at?: string
  // Feature 029: how the agent enrolled.
  enrolled_via?: 'token' | 'auto'
  auto_enroll_key_id?: string
}

export interface AgentFleet extends PageInfo {
  items: AgentFleetEntry[]
  current_version?: string
}

export interface AgentUpgrade {
  id: string
  agent_id: string
  host_id?: string
  from_version?: string
  target_version: string
  allow_downgrade?: boolean
  state: UpgradeState
  origin: 'user' | 'policy' | 'agent'
  requested_by?: string
  reason?: string
  attempts?: number
  created_at: string
  updated_at?: string
  expires_at?: string
  finished_at?: string
}

export type SkipReason = 'up_to_date' | 'upgrade_active' | 'manual_upgrade_required' | 'unsupported' | 'no_release_for_platform' | 'not_found'

export interface UpgradeBatchResult {
  target_version: string
  created: AgentUpgrade[]
  skipped: { agent_id: string; reason: SkipReason }[]
}

export interface AgentReleaseInfo {
  version: string
  source: 'bundled' | 'import'
  key_id: string
  imported_at: string
  platforms: { os: string; arch: string; install_type: string; size: number }[]
}

export interface AgentReleaseList {
  current_version?: string
  items: AgentReleaseInfo[]
}

// MintedToken is the one-time secret returned by /agents/enroll-token.
export interface MintedToken {
  id: string
  token: string
  expires_at: string
  label?: string
}

export interface RefreshResult {
  delivered: boolean
  command_id?: string
}

// --- statistics ---

// Stats mirrors the /statistics/tenant response.
export interface Stats {
  hosts_total: number
  hosts_by_status: Record<string, number>
  hosts_by_os: Record<string, number>
  hosts_by_manufacturer: Record<string, number>
  agents_online: number
  agents_offline: number
  stale_hosts: number
  snapshots_total: number
  total_ram_bytes: number
  total_cpu_cores: number
  total_disk_bytes: number
}

// UpgradePolicy is the tenant's automatic agent upgrade policy.
export interface UpgradePolicy {
  enabled: boolean
  window_start: string
  window_end: string
  timezone: string
  max_concurrent: number
  target_version: string
  paused?: boolean
  paused_reason?: string
  updated_by?: string
  updated_at?: string
}

// --- automatic enrollment (feature 029) ---

export type AutoEnrollKeyState = 'active' | 'disabled' | 'expired' | 'exhausted'

// AutoEnrollKey is a reusable enrollment key; its secret is never listed.
export interface AutoEnrollKey {
  id: string
  key_id: string
  name: string
  allowed_cidrs: string[]
  enabled: boolean
  state: AutoEnrollKeyState
  expires_at: string | null
  max_enrollments: number
  enrollments: number
  last_used_at: string | null
  last_used_ip: string
  created_by: string
  created_at: string
  updated_at: string
}

// AutoEnroll is the tenant switch with its keys.
export interface AutoEnroll {
  enabled: boolean
  updated_by?: string
  updated_at?: string | null
  window_seconds: number
  /** One page of the keys (list contract fields alongside). */
  keys: AutoEnrollKey[]
  total?: number
  page?: number
  page_size?: number
  sort?: string
  order?: 'asc' | 'desc'
}

// AutoEnrollKeySecret is returned by create and rotate: the only time the secret is shown.
export interface AutoEnrollKeySecret {
  key: AutoEnrollKey
  secret: string
}

export interface AutoEnrollKeyInput {
  name: string
  allowed_cidrs: string[]
  expires_at: string | null
  max_enrollments: number
}
