-- Credential references: store pointers to external secret stores
-- instead of encrypted credentials directly.
--
-- password_ref / extra_ref hold URI-scheme refs like:
--   env://PG_PASSWORD
--   vault://secret/data/prod-pg#password
--   k8s://brokoli/db-creds/password
--   encrypted://<base64 AES-GCM ciphertext>
--
-- Legacy password_enc / extra_enc columns are preserved for rollback.
-- The migration code in Go copies existing encrypted values into the
-- new columns with the encrypted:// prefix.

ALTER TABLE connections ADD COLUMN IF NOT EXISTS password_ref TEXT NOT NULL DEFAULT '';
ALTER TABLE connections ADD COLUMN IF NOT EXISTS extra_ref TEXT NOT NULL DEFAULT '';
