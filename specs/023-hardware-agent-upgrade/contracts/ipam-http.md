# Contract: IPAM HTTP and proto changes for device hardware (023)

Repository `go-tangra-ipam-v4`. `api/openapi/ipam.yaml` (embedded; routes
registered through `pkg/ipammanifest`). No new permission: reads use the
existing `ipam:read`; there is **no write endpoint** for hardware (FR-008).

## Endpoints

| Method | Path | Permission | Result |
|---|---|---|---|
| GET | `/api/ipam/v1/devices/{id}/hardware` | `ipam:read` | `DeviceHardware`; `404 not_found` when the device has no stored hardware |
| GET | `/api/ipam/v1/devices/{id}` (changed, additive) | `ipam:read` | `Device` + read-only `hardware_summary` |
| GET | `/api/ipam/v1/devices` (changed, additive) | `ipam:read` | items gain `hardware_summary`; new filter `has_hardware=true|false` |

Device create/update bodies (`additionalProperties: false`) do **not**
accept `hardware_summary`/`hardware`; a request carrying them is rejected
with 400 (strict decode), and the gRPC `CreateDevice`/`UpdateDevice` ignore
them (server-owned, like the 020 host-sync fields).

## Schemas

```yaml
HardwareSummary:
  type: object
  readOnly: true
  properties:
    cpu_model:          { type: string }
    cpu_sockets:        { type: integer }
    cpu_cores:          { type: integer }
    cpu_threads:        { type: integer }
    memory_total_bytes: { type: integer, format: int64 }
    memory_type:        { type: string }
    memory_slots_total: { type: integer }
    memory_slots_used:  { type: integer }
    disk_count:         { type: integer }
    disk_total_bytes:   { type: integer, format: int64 }
    reported_at:        { type: string, format: date-time }

DeviceHardware:
  type: object
  readOnly: true
  properties:
    device_id:   { type: string, format: uuid }
    reported_at: { type: string, format: date-time }
    summary:     { $ref: '#/components/schemas/HardwareSummary' }
    bios:        { type: object, properties: { vendor: {type: string}, version: {type: string}, release_date: {type: string} } }
    system:      { type: object, properties: { manufacturer: {type: string}, product: {type: string}, version: {type: string}, serial: {type: string}, uuid: {type: string}, sku: {type: string}, family: {type: string} } }
    board:       { type: object, properties: { manufacturer: {type: string}, product: {type: string}, serial: {type: string} } }
    chassis:     { type: object, properties: { type: {type: string}, manufacturer: {type: string}, serial: {type: string}, asset_tag: {type: string} } }
    processors:  { type: array, maxItems: 256, items: { type: object, properties: {
                     socket: {type: string}, manufacturer: {type: string}, model: {type: string}, family: {type: string},
                     max_mhz: {type: integer}, current_mhz: {type: integer}, cores: {type: integer}, threads: {type: integer}, populated: {type: boolean} } } }
    memory:      { type: object, properties: {
                     total_bytes: {type: integer, format: int64}, error_correction: {type: string}, location: {type: string}, use: {type: string},
                     max_capacity_bytes: {type: integer, format: int64}, slots_total: {type: integer}, slots_used: {type: integer},
                     slots: { type: array, maxItems: 1024, items: { type: object, properties: {
                       locator: {type: string}, bank: {type: string}, populated: {type: boolean}, size_bytes: {type: integer, format: int64},
                       type: {type: string}, form_factor: {type: string}, type_detail: {type: array, items: {type: string}},
                       speed_mts: {type: integer}, configured_mts: {type: integer}, manufacturer: {type: string}, part_number: {type: string}, serial: {type: string} } } } } }
    disks:       { type: array, maxItems: 256, items: { type: object, properties: {
                     name: {type: string}, model: {type: string}, vendor: {type: string}, serial: {type: string}, size_bytes: {type: integer, format: int64},
                     media: {type: string, enum: [ssd, hdd, nvme_ssd, unknown]},
                     interface: {type: string, enum: [nvme, sata, sas, scsi, usb, virtio, hyperv, xen, mmc, other]}, removable: {type: boolean} } } }
    filesystems: { type: array, maxItems: 1024, items: { type: object, properties: {
                     mount: {type: string}, fs: {type: string}, size_bytes: {type: integer, format: int64}, free_bytes: {type: integer, format: int64},
                     disks: {type: array, maxItems: 64, items: {type: string}} } } }
    availability: { type: object, properties: { smbios: {type: string}, disks: {type: string} } }
    truncated:   { type: object, additionalProperties: { type: integer } }
```

## Proto (`go-tangra-ipam-v4/sdk/api/proto/ipam/v1/ipam.proto`)

Additive: `message HardwareSummary { … }` (fields as above) and
`Device.hardware_summary = <next free number>`; ipam SDK **`sdk/v4.2.0`**
(only if the ipam SDK is released with this feature; otherwise the field
waits for the next SDK release — the HTTP API is the UI contract).

## UI (`go-tangra-ipam-v4/ui/src/views/devices/detail.vue`)

- Tab `hardware` ("Hardware") shown when `device.hardware_summary` is set;
  for host-reported devices without hardware an empty state "Hardware is
  reported by inventory agents 4.4.0 or newer" (FR-007 scenario 3); not
  shown for manual/scan devices.
- Summary row "Hardware": `2× Intel Xeon Silver 4310 · 24 cores / 48
  threads · 512 GiB DDR4 (16/16 slots) · 3 disks, 11.8 TB`.
- Cards: BIOS; System / Board / Chassis; Processors table; Memory (total,
  ECC, slots table incl. empty slots greyed); Disks table (removable
  badge) with filesystems (usage bar, disk column). All reported strings
  are rendered as text (no `v-html`).
