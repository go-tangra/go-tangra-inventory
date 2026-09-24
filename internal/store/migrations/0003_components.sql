-- +goose Up
-- Normalized component child tables. Populated on ingest from the snapshot
-- payload (the payload stays authoritative); these exist so components are
-- directly queryable and aggregatable. Each row carries tenant_id + host_id +
-- snapshot_id. No FK to inventory_snapshots (a hypertable cannot be the target
-- of a foreign key); host_id FKs inventory_hosts so a host delete cascades and
-- retention purge deletes child rows by snapshot_id explicitly.

CREATE TABLE inventory_processors (
  id                 uuid PRIMARY KEY,
  tenant_id          uuid NOT NULL,
  host_id            uuid NOT NULL REFERENCES inventory_hosts(id) ON DELETE CASCADE,
  snapshot_id        uuid NOT NULL,
  socket_designation text NOT NULL DEFAULT '',
  manufacturer       text NOT NULL DEFAULT '',
  version            text NOT NULL DEFAULT '',
  max_speed_mhz      bigint NOT NULL DEFAULT 0,
  current_speed_mhz  bigint NOT NULL DEFAULT 0,
  core_count         bigint NOT NULL DEFAULT 0,
  core_enabled       bigint NOT NULL DEFAULT 0,
  thread_count       bigint NOT NULL DEFAULT 0,
  part_number        text NOT NULL DEFAULT '',
  serial_number      text NOT NULL DEFAULT '',
  socket_populated   boolean NOT NULL DEFAULT false
);
CREATE INDEX processors_snapshot ON inventory_processors (tenant_id, snapshot_id);
CREATE INDEX processors_serial   ON inventory_processors (tenant_id, serial_number);

CREATE TABLE inventory_memory_modules (
  id                    uuid PRIMARY KEY,
  tenant_id             uuid NOT NULL,
  host_id               uuid NOT NULL REFERENCES inventory_hosts(id) ON DELETE CASCADE,
  snapshot_id           uuid NOT NULL,
  device_locator        text NOT NULL DEFAULT '',
  bank_locator          text NOT NULL DEFAULT '',
  capacity_bytes        bigint NOT NULL DEFAULT 0,
  form_factor           text NOT NULL DEFAULT '',
  memory_type           text NOT NULL DEFAULT '',
  speed_mt_s            bigint NOT NULL DEFAULT 0,
  configured_speed_mt_s bigint NOT NULL DEFAULT 0,
  manufacturer          text NOT NULL DEFAULT '',
  serial_number         text NOT NULL DEFAULT '',
  part_number           text NOT NULL DEFAULT ''
);
CREATE INDEX memory_modules_snapshot ON inventory_memory_modules (tenant_id, snapshot_id);
CREATE INDEX memory_modules_serial   ON inventory_memory_modules (tenant_id, serial_number);

CREATE TABLE inventory_disks (
  id           uuid PRIMARY KEY,
  tenant_id    uuid NOT NULL,
  host_id      uuid NOT NULL REFERENCES inventory_hosts(id) ON DELETE CASCADE,
  snapshot_id  uuid NOT NULL,
  model        text NOT NULL DEFAULT '',
  serial       text NOT NULL DEFAULT '',
  size_bytes   bigint NOT NULL DEFAULT 0,
  media_type   text NOT NULL DEFAULT '',
  interface    text NOT NULL DEFAULT ''
);
CREATE INDEX disks_snapshot ON inventory_disks (tenant_id, snapshot_id);
CREATE INDEX disks_serial   ON inventory_disks (tenant_id, serial);

CREATE TABLE inventory_network_interfaces (
  id           uuid PRIMARY KEY,
  tenant_id    uuid NOT NULL,
  host_id      uuid NOT NULL REFERENCES inventory_hosts(id) ON DELETE CASCADE,
  snapshot_id  uuid NOT NULL,
  name         text NOT NULL DEFAULT '',
  mac          text NOT NULL DEFAULT '',
  ip_addresses jsonb NOT NULL DEFAULT '[]'::jsonb,
  subnet       text NOT NULL DEFAULT '',
  gateway      text NOT NULL DEFAULT '',
  dns          jsonb NOT NULL DEFAULT '[]'::jsonb,
  dhcp         boolean NOT NULL DEFAULT false,
  speed_bps    bigint NOT NULL DEFAULT 0,
  type         text NOT NULL DEFAULT '',
  up           boolean NOT NULL DEFAULT false
);
CREATE INDEX nics_snapshot ON inventory_network_interfaces (tenant_id, snapshot_id);
CREATE INDEX nics_mac      ON inventory_network_interfaces (tenant_id, mac);

CREATE TABLE inventory_software (
  id               uuid PRIMARY KEY,
  tenant_id        uuid NOT NULL,
  host_id          uuid NOT NULL REFERENCES inventory_hosts(id) ON DELETE CASCADE,
  snapshot_id      uuid NOT NULL,
  name             text NOT NULL DEFAULT '',
  version          text NOT NULL DEFAULT '',
  publisher        text NOT NULL DEFAULT '',
  install_date     text NOT NULL DEFAULT '',
  install_location text NOT NULL DEFAULT '',
  size_bytes       bigint NOT NULL DEFAULT 0
);
CREATE INDEX software_snapshot ON inventory_software (tenant_id, snapshot_id);
CREATE INDEX software_name_ver ON inventory_software (tenant_id, name, version);

CREATE TABLE inventory_services (
  id           uuid PRIMARY KEY,
  tenant_id    uuid NOT NULL,
  host_id      uuid NOT NULL REFERENCES inventory_hosts(id) ON DELETE CASCADE,
  snapshot_id  uuid NOT NULL,
  name         text NOT NULL DEFAULT '',
  display_name text NOT NULL DEFAULT '',
  state        text NOT NULL DEFAULT '',
  start_mode   text NOT NULL DEFAULT '',
  account      text NOT NULL DEFAULT ''
);
CREATE INDEX services_snapshot ON inventory_services (tenant_id, snapshot_id);
CREATE INDEX services_name     ON inventory_services (tenant_id, name);

CREATE TABLE inventory_monitors (
  id            uuid PRIMARY KEY,
  tenant_id     uuid NOT NULL,
  host_id       uuid NOT NULL REFERENCES inventory_hosts(id) ON DELETE CASCADE,
  snapshot_id   uuid NOT NULL,
  manufacturer  text NOT NULL DEFAULT '',
  model         text NOT NULL DEFAULT '',
  serial_number text NOT NULL DEFAULT ''
);
CREATE INDEX monitors_snapshot ON inventory_monitors (tenant_id, snapshot_id);
CREATE INDEX monitors_serial   ON inventory_monitors (tenant_id, serial_number);

-- +goose Down
DROP TABLE IF EXISTS inventory_monitors;
DROP TABLE IF EXISTS inventory_services;
DROP TABLE IF EXISTS inventory_software;
DROP TABLE IF EXISTS inventory_network_interfaces;
DROP TABLE IF EXISTS inventory_disks;
DROP TABLE IF EXISTS inventory_memory_modules;
DROP TABLE IF EXISTS inventory_processors;
