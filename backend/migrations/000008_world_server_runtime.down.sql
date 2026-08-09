DROP INDEX IF EXISTS instances_active_expiration_idx;
DROP INDEX IF EXISTS instances_world_server_credential_id_idx;

ALTER TABLE instances
  DROP COLUMN IF EXISTS last_heartbeat_at,
  DROP COLUMN IF EXISTS world_server_credential_id;
