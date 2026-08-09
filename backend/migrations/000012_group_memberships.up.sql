ALTER TABLE groups
  ADD COLUMN owner_actor_id UUID NULL REFERENCES actors(id) ON DELETE RESTRICT;

CREATE TABLE group_members (
  group_id UUID NOT NULL REFERENCES groups(id) ON DELETE CASCADE,
  actor_id UUID NOT NULL REFERENCES actors(id) ON DELETE CASCADE,
  role TEXT NOT NULL DEFAULT 'member' CHECK (role IN ('owner', 'moderator', 'member')),
  state TEXT NOT NULL DEFAULT 'pending' CHECK (state IN ('pending', 'active', 'banned')),
  created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  PRIMARY KEY (group_id, actor_id)
);

CREATE TRIGGER group_members_set_updated_at
BEFORE UPDATE ON group_members
FOR EACH ROW EXECUTE FUNCTION set_updated_at();

CREATE INDEX group_members_actor_state_idx
  ON group_members(actor_id, state, group_id);
CREATE INDEX group_members_group_state_created_idx
  ON group_members(group_id, state, created_at DESC, actor_id DESC);

CREATE TABLE group_worlds (
  group_id UUID NOT NULL REFERENCES groups(id) ON DELETE CASCADE,
  world_id UUID NOT NULL REFERENCES worlds(id) ON DELETE CASCADE,
  added_by_actor_id UUID NOT NULL REFERENCES actors(id) ON DELETE RESTRICT,
  created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  PRIMARY KEY (group_id, world_id)
);

CREATE TABLE group_events (
  group_id UUID NOT NULL REFERENCES groups(id) ON DELETE CASCADE,
  event_id UUID NOT NULL REFERENCES events(id) ON DELETE CASCADE,
  added_by_actor_id UUID NOT NULL REFERENCES actors(id) ON DELETE RESTRICT,
  created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  PRIMARY KEY (group_id, event_id)
);

CREATE INDEX group_worlds_world_idx ON group_worlds(world_id, group_id);
CREATE INDEX group_events_event_idx ON group_events(event_id, group_id);
CREATE INDEX groups_owner_idx ON groups(owner_actor_id, created_at DESC);
CREATE INDEX groups_public_created_idx ON groups(created_at DESC, id DESC)
  WHERE visibility = 'public';
