package store

const createTableSQL = `
CREATE TABLE IF NOT EXISTS inventories (
    id              INTEGER PRIMARY KEY AUTOINCREMENT,
    hostname        TEXT NOT NULL,
    username        TEXT NOT NULL DEFAULT '',
    system_uuid     TEXT NOT NULL DEFAULT '',
    system_serial   TEXT NOT NULL DEFAULT '',
    collected_at    TEXT NOT NULL,
    stored_at       TEXT NOT NULL,
    inventory_json  TEXT NOT NULL
);

CREATE INDEX IF NOT EXISTS idx_inventories_hostname ON inventories(hostname);
CREATE INDEX IF NOT EXISTS idx_inventories_system_uuid ON inventories(system_uuid);
CREATE INDEX IF NOT EXISTS idx_inventories_collected_at ON inventories(collected_at);
CREATE INDEX IF NOT EXISTS idx_inventories_username ON inventories(username);
`
