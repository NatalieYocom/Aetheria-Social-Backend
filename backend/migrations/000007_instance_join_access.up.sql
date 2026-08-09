CREATE TABLE world_server_credentials (
  id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
  name TEXT NOT NULL UNIQUE,
  token_prefix TEXT NOT NULL UNIQUE,
  token_hash TEXT NOT NULL UNIQUE,
  allowed_world_id UUID NULL REFERENCES worlds(id) ON DELETE SET NULL,
  status TEXT NOT NULL DEFAULT 'active' CHECK (status IN ('active', 'revoked')),
  created_by_user_id UUID NOT NULL REFERENCES users(id) ON DELETE RESTRICT,
  metadata JSONB NOT NULL DEFAULT '{}'::jsonb,
  last_used_at TIMESTAMPTZ NULL,
  revoked_at TIMESTAMPTZ NULL,
  created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX world_server_credentials_status_idx
  ON world_server_credentials(status);

CREATE TABLE instance_join_tickets (
  id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
  token_hash TEXT NOT NULL UNIQUE,
  actor_id UUID NOT NULL REFERENCES actors(id) ON DELETE CASCADE,
  instance_id UUID NOT NULL REFERENCES instances(id) ON DELETE CASCADE,
  presence_visibility TEXT NOT NULL DEFAULT 'friends'
    CHECK (presence_visibility IN ('nobody', 'friends', 'followers', 'public')),
  show_exact_instance BOOLEAN NOT NULL DEFAULT false,
  metadata JSONB NOT NULL DEFAULT '{}'::jsonb,
  expires_at TIMESTAMPTZ NOT NULL,
  consumed_at TIMESTAMPTZ NULL,
  consumed_by_credential_id UUID NULL
    REFERENCES world_server_credentials(id) ON DELETE SET NULL,
  created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX instance_join_tickets_actor_active_idx
  ON instance_join_tickets(actor_id, expires_at)
  WHERE consumed_at IS NULL;

CREATE INDEX instance_join_tickets_instance_active_idx
  ON instance_join_tickets(instance_id, expires_at)
  WHERE consumed_at IS NULL;

CREATE TABLE instance_join_audit (
  id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
  ticket_id UUID NULL REFERENCES instance_join_tickets(id) ON DELETE SET NULL,
  credential_id UUID NULL REFERENCES world_server_credentials(id) ON DELETE SET NULL,
  actor_id UUID NULL REFERENCES actors(id) ON DELETE SET NULL,
  instance_id UUID NULL REFERENCES instances(id) ON DELETE SET NULL,
  outcome TEXT NOT NULL,
  remote_ip TEXT NOT NULL DEFAULT '',
  details JSONB NOT NULL DEFAULT '{}'::jsonb,
  created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX instance_join_audit_created_at_idx
  ON instance_join_audit(created_at DESC);

CREATE INDEX instance_join_audit_outcome_idx
  ON instance_join_audit(outcome, created_at DESC);
