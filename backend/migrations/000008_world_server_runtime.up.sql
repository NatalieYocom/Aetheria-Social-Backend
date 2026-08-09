ALTER TABLE instances
  ADD COLUMN world_server_credential_id UUID NULL
    REFERENCES world_server_credentials(id) ON DELETE SET NULL,
  ADD COLUMN last_heartbeat_at TIMESTAMPTZ NULL;

CREATE INDEX instances_world_server_credential_id_idx
  ON instances(world_server_credential_id)
  WHERE world_server_credential_id IS NOT NULL;

CREATE INDEX instances_active_expiration_idx
  ON instances(expires_at)
  WHERE status = 'active' AND expires_at IS NOT NULL;
