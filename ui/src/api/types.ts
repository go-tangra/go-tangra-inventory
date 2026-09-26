// Domain types mirror the inventory OpenAPI responses (api/openapi/inventory.yaml)
// and the store models (internal/store/models.go). Credentials and sealed fields
// are never returned in listings and are omitted here. Response projections use
// optional (`?:`) fields; inputs/filters use explicit `T | undefined` to satisfy
// exactOptionalPropertyTypes.

export type HostStatus = 'active' | 'stale' | 'retired'
export type SnapshotSource = 'agent' | 'manual' | 'import'
export type ChangeType = 'added' | 'removed' | 'modified'

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
  type?: string
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
}

export interface MemoryArray {
  location?: string
  use?: string
  error_correction?: string
  maximum_capacity?: number
  number_of_devices?: number
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
}

export interface MemoryInfo {
  total_physical_bytes?: number
  array: MemoryArray
  modules?: MemoryModule[]
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
}

export interface Partition {
  mount?: string
  fs?: string
  size_bytes?: number
  free_bytes?: number
}

export interface Disk {
  model?: string
  serial?: string
  size_bytes?: number
  media_type?: string
  interface?: string
  partitions?: Partition[]
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
