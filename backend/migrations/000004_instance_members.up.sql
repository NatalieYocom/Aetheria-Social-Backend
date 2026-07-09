CREATE TABLE instance_members (
  instance_id UUID NOT NULL REFERENCES instances(id) ON DELETE CASCADE,
  actor_id UUID NOT NULL REFERENCES actors(id) ON DELETE CASCADE,
  state TEXT NOT NULL DEFAULT 'joined' CHECK (state IN ('joined', 'left')),
  joined_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  last_seen_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  left_at TIMESTAMPTZ NULL,
  metadata JSONB NOT NULL DEFAULT '{}'::jsonb,
  PRIMARY KEY (instance_id, actor_id)
);

CREATE INDEX instance_members_actor_state_idx ON instance_members(actor_id, state);
CREATE INDEX instance_members_instance_state_idx ON instance_members(instance_id, state);
