-- +goose Up
-- Feature 033 (US4): the subject common name of the leaf an item served,
-- recorded at fetch time like its serial and fingerprint, so the host
-- certificate keeps the CN of the certificate actually installed. Identity
-- only, never material; bounded like inventory_host_certificates.common_name.
ALTER TABLE inventory_cert_delivery_items
  ADD COLUMN common_name text NOT NULL DEFAULT '' CHECK (octet_length(common_name) <= 256);

-- +goose Down
ALTER TABLE inventory_cert_delivery_items DROP COLUMN IF EXISTS common_name;
