CREATE EXTENSION IF NOT EXISTS pgcrypto;

CREATE OR REPLACE FUNCTION set_updated_at()
RETURNS trigger AS $$
BEGIN
  NEW.updated_at = now();
  RETURN NEW;
END;
$$ LANGUAGE plpgsql;

CREATE TABLE users (
  id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
  email TEXT NOT NULL UNIQUE,
  password_hash TEXT NOT NULL,
  username TEXT NOT NULL UNIQUE,
  status TEXT NOT NULL DEFAULT 'active' CHECK (status IN ('active', 'suspended', 'deleted')),
  created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TRIGGER users_set_updated_at
BEFORE UPDATE ON users
FOR EACH ROW EXECUTE FUNCTION set_updated_at();

CREATE TABLE profiles (
  id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
  user_id UUID NOT NULL UNIQUE REFERENCES users(id) ON DELETE CASCADE,
  display_name TEXT NOT NULL DEFAULT '',
  bio TEXT NOT NULL DEFAULT '',
  avatar_url TEXT NOT NULL DEFAULT '',
  banner_url TEXT NOT NULL DEFAULT '',
  status_text TEXT NOT NULL DEFAULT '',
  links JSONB NOT NULL DEFAULT '[]'::jsonb,
  privacy_settings JSONB NOT NULL DEFAULT '{}'::jsonb,
  created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TRIGGER profiles_set_updated_at
BEFORE UPDATE ON profiles
FOR EACH ROW EXECUTE FUNCTION set_updated_at();

CREATE TABLE actors (
  id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
  local_user_id UUID UNIQUE NULL REFERENCES users(id) ON DELETE CASCADE,
  actor_uri TEXT NOT NULL UNIQUE,
  acct TEXT NOT NULL UNIQUE,
  type TEXT NOT NULL CHECK (type IN ('Person', 'Group', 'Service')),
  preferred_username TEXT NOT NULL,
  display_name TEXT NOT NULL DEFAULT '',
  domain TEXT NOT NULL,
  inbox_url TEXT NOT NULL,
  outbox_url TEXT NOT NULL,
  followers_url TEXT NOT NULL,
  following_url TEXT NOT NULL,
  shared_inbox_url TEXT NULL,
  public_key_pem TEXT NOT NULL DEFAULT '',
  private_key_pem_encrypted TEXT NULL,
  is_local BOOLEAN NOT NULL DEFAULT false,
  raw_json JSONB NOT NULL DEFAULT '{}'::jsonb,
  last_fetched_at TIMESTAMPTZ NULL,
  created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX actors_acct_idx ON actors(acct);
CREATE INDEX actors_domain_idx ON actors(domain);
CREATE INDEX actors_local_user_id_idx ON actors(local_user_id);

CREATE TRIGGER actors_set_updated_at
BEFORE UPDATE ON actors
FOR EACH ROW EXECUTE FUNCTION set_updated_at();

CREATE TABLE relationships (
  id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
  actor_id UUID NOT NULL REFERENCES actors(id) ON DELETE CASCADE,
  target_actor_id UUID NOT NULL REFERENCES actors(id) ON DELETE CASCADE,
  type TEXT NOT NULL CHECK (type IN ('follow', 'friend', 'block', 'mute')),
  direction TEXT NOT NULL CHECK (direction IN ('outgoing', 'incoming', 'mutual')),
  state TEXT NOT NULL CHECK (state IN ('pending', 'accepted', 'rejected', 'removed')),
  created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  CONSTRAINT relationships_not_self CHECK (actor_id <> target_actor_id),
  CONSTRAINT relationships_unique_edge UNIQUE (actor_id, target_actor_id, type)
);

CREATE INDEX relationships_actor_id_idx ON relationships(actor_id);
CREATE INDEX relationships_target_actor_id_idx ON relationships(target_actor_id);
CREATE INDEX relationships_type_state_idx ON relationships(type, state);

CREATE TRIGGER relationships_set_updated_at
BEFORE UPDATE ON relationships
FOR EACH ROW EXECUTE FUNCTION set_updated_at();

CREATE TABLE worlds (
  id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
  owner_actor_id UUID NOT NULL REFERENCES actors(id) ON DELETE CASCADE,
  slug TEXT NOT NULL UNIQUE,
  name TEXT NOT NULL,
  description TEXT NOT NULL DEFAULT '',
  preview_url TEXT NOT NULL DEFAULT '',
  launch_url TEXT NOT NULL DEFAULT '',
  visibility TEXT NOT NULL DEFAULT 'public' CHECK (visibility IN ('public', 'followers', 'friends', 'private')),
  capacity INT NOT NULL DEFAULT 0 CHECK (capacity >= 0),
  tags TEXT[] NOT NULL DEFAULT '{}',
  metadata JSONB NOT NULL DEFAULT '{}'::jsonb,
  created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX worlds_owner_actor_id_idx ON worlds(owner_actor_id);
CREATE INDEX worlds_visibility_idx ON worlds(visibility);
CREATE INDEX worlds_tags_idx ON worlds USING GIN(tags);

CREATE TRIGGER worlds_set_updated_at
BEFORE UPDATE ON worlds
FOR EACH ROW EXECUTE FUNCTION set_updated_at();

CREATE TABLE world_favorites (
  actor_id UUID NOT NULL REFERENCES actors(id) ON DELETE CASCADE,
  world_id UUID NOT NULL REFERENCES worlds(id) ON DELETE CASCADE,
  created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  PRIMARY KEY (actor_id, world_id)
);

CREATE TABLE events (
  id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
  owner_actor_id UUID NOT NULL REFERENCES actors(id) ON DELETE CASCADE,
  world_id UUID NOT NULL REFERENCES worlds(id) ON DELETE CASCADE,
  slug TEXT NOT NULL UNIQUE,
  name TEXT NOT NULL,
  description TEXT NOT NULL DEFAULT '',
  start_time TIMESTAMPTZ NOT NULL,
  end_time TIMESTAMPTZ NOT NULL,
  visibility TEXT NOT NULL DEFAULT 'public' CHECK (visibility IN ('public', 'followers', 'friends', 'private')),
  launch_url TEXT NOT NULL DEFAULT '',
  metadata JSONB NOT NULL DEFAULT '{}'::jsonb,
  created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  CONSTRAINT events_time_order CHECK (end_time > start_time)
);

CREATE INDEX events_owner_actor_id_idx ON events(owner_actor_id);
CREATE INDEX events_world_id_idx ON events(world_id);
CREATE INDEX events_start_time_idx ON events(start_time);
CREATE INDEX events_visibility_idx ON events(visibility);

CREATE TRIGGER events_set_updated_at
BEFORE UPDATE ON events
FOR EACH ROW EXECUTE FUNCTION set_updated_at();

CREATE TABLE event_rsvps (
  actor_id UUID NOT NULL REFERENCES actors(id) ON DELETE CASCADE,
  event_id UUID NOT NULL REFERENCES events(id) ON DELETE CASCADE,
  state TEXT NOT NULL CHECK (state IN ('going', 'interested', 'declined')),
  created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  PRIMARY KEY (actor_id, event_id)
);

CREATE TRIGGER event_rsvps_set_updated_at
BEFORE UPDATE ON event_rsvps
FOR EACH ROW EXECUTE FUNCTION set_updated_at();

CREATE TABLE instances (
  id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
  world_id UUID NOT NULL REFERENCES worlds(id) ON DELETE CASCADE,
  host_actor_id UUID NOT NULL REFERENCES actors(id) ON DELETE CASCADE,
  instance_key TEXT NOT NULL UNIQUE,
  name TEXT NOT NULL,
  visibility TEXT NOT NULL DEFAULT 'public' CHECK (visibility IN ('public', 'friends', 'invite_only', 'private')),
  launch_url TEXT NOT NULL DEFAULT '',
  capacity INT NOT NULL DEFAULT 0 CHECK (capacity >= 0),
  current_users INT NOT NULL DEFAULT 0 CHECK (current_users >= 0),
  status TEXT NOT NULL DEFAULT 'active' CHECK (status IN ('active', 'closed', 'expired')),
  expires_at TIMESTAMPTZ NULL,
  metadata JSONB NOT NULL DEFAULT '{}'::jsonb,
  created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX instances_world_id_idx ON instances(world_id);
CREATE INDEX instances_host_actor_id_idx ON instances(host_actor_id);
CREATE INDEX instances_status_idx ON instances(status);

CREATE TRIGGER instances_set_updated_at
BEFORE UPDATE ON instances
FOR EACH ROW EXECUTE FUNCTION set_updated_at();

CREATE TABLE invites (
  id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
  from_actor_id UUID NOT NULL REFERENCES actors(id) ON DELETE CASCADE,
  to_actor_id UUID NOT NULL REFERENCES actors(id) ON DELETE CASCADE,
  instance_id UUID NULL REFERENCES instances(id) ON DELETE SET NULL,
  world_id UUID NULL REFERENCES worlds(id) ON DELETE SET NULL,
  event_id UUID NULL REFERENCES events(id) ON DELETE SET NULL,
  message TEXT NOT NULL DEFAULT '',
  visibility TEXT NOT NULL DEFAULT 'direct' CHECK (visibility IN ('direct', 'friends', 'followers', 'public')),
  state TEXT NOT NULL DEFAULT 'pending' CHECK (state IN ('pending', 'accepted', 'declined', 'expired')),
  expires_at TIMESTAMPTZ NOT NULL,
  created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX invites_from_actor_id_idx ON invites(from_actor_id);
CREATE INDEX invites_to_actor_id_idx ON invites(to_actor_id);
CREATE INDEX invites_state_idx ON invites(state);

CREATE TRIGGER invites_set_updated_at
BEFORE UPDATE ON invites
FOR EACH ROW EXECUTE FUNCTION set_updated_at();

CREATE TABLE presence_sessions (
  id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
  actor_id UUID NOT NULL UNIQUE REFERENCES actors(id) ON DELETE CASCADE,
  world_id UUID NULL REFERENCES worlds(id) ON DELETE SET NULL,
  instance_id UUID NULL REFERENCES instances(id) ON DELETE SET NULL,
  status TEXT NOT NULL DEFAULT 'online' CHECK (status IN ('online', 'away', 'busy', 'invisible')),
  visibility TEXT NOT NULL DEFAULT 'nobody' CHECK (visibility IN ('nobody', 'friends', 'followers', 'public')),
  show_exact_instance BOOLEAN NOT NULL DEFAULT false,
  expires_at TIMESTAMPTZ NOT NULL,
  updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX presence_sessions_actor_id_idx ON presence_sessions(actor_id);
CREATE INDEX presence_sessions_expires_at_idx ON presence_sessions(expires_at);
CREATE INDEX presence_sessions_visibility_idx ON presence_sessions(visibility);

CREATE TABLE groups (
  id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
  actor_id UUID NOT NULL UNIQUE REFERENCES actors(id) ON DELETE CASCADE,
  slug TEXT NOT NULL UNIQUE,
  name TEXT NOT NULL,
  description TEXT NOT NULL DEFAULT '',
  avatar_url TEXT NOT NULL DEFAULT '',
  banner_url TEXT NOT NULL DEFAULT '',
  visibility TEXT NOT NULL DEFAULT 'public' CHECK (visibility IN ('public', 'private', 'invite_only')),
  metadata JSONB NOT NULL DEFAULT '{}'::jsonb,
  created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TRIGGER groups_set_updated_at
BEFORE UPDATE ON groups
FOR EACH ROW EXECUTE FUNCTION set_updated_at();

CREATE TABLE activities (
  id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
  activity_uri TEXT NOT NULL UNIQUE,
  actor_id UUID NOT NULL REFERENCES actors(id) ON DELETE CASCADE,
  type TEXT NOT NULL,
  object_uri TEXT NULL,
  object_id UUID NULL,
  object_type TEXT NULL,
  visibility TEXT NOT NULL DEFAULT 'public',
  raw_json JSONB NOT NULL DEFAULT '{}'::jsonb,
  direction TEXT NOT NULL CHECK (direction IN ('inbound', 'outbound', 'local')),
  published_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  received_at TIMESTAMPTZ NULL,
  created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX activities_actor_id_idx ON activities(actor_id);
CREATE INDEX activities_activity_uri_idx ON activities(activity_uri);
CREATE INDEX activities_object_idx ON activities(object_type, object_id);

CREATE TABLE inbox_messages (
  id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
  recipient_actor_id UUID NOT NULL REFERENCES actors(id) ON DELETE CASCADE,
  sender_actor_id UUID NULL REFERENCES actors(id) ON DELETE SET NULL,
  activity_uri TEXT NOT NULL,
  type TEXT NOT NULL,
  raw_json JSONB NOT NULL DEFAULT '{}'::jsonb,
  signature_valid BOOLEAN NOT NULL DEFAULT false,
  processing_state TEXT NOT NULL DEFAULT 'pending' CHECK (processing_state IN ('pending', 'processed', 'failed', 'ignored')),
  error TEXT NULL,
  created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  processed_at TIMESTAMPTZ NULL
);

CREATE INDEX inbox_messages_activity_uri_idx ON inbox_messages(activity_uri);
CREATE INDEX inbox_messages_recipient_actor_id_idx ON inbox_messages(recipient_actor_id);
CREATE INDEX inbox_messages_processing_state_idx ON inbox_messages(processing_state);

CREATE TABLE outbox_jobs (
  id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
  activity_id UUID NOT NULL REFERENCES activities(id) ON DELETE CASCADE,
  target_inbox_url TEXT NOT NULL,
  state TEXT NOT NULL DEFAULT 'pending' CHECK (state IN ('pending', 'delivered', 'failed', 'retry')),
  attempts INT NOT NULL DEFAULT 0 CHECK (attempts >= 0),
  next_retry_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  last_error TEXT NULL,
  created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX outbox_jobs_state_retry_idx ON outbox_jobs(state, next_retry_at);
CREATE INDEX outbox_jobs_activity_id_idx ON outbox_jobs(activity_id);

CREATE TRIGGER outbox_jobs_set_updated_at
BEFORE UPDATE ON outbox_jobs
FOR EACH ROW EXECUTE FUNCTION set_updated_at();

CREATE TABLE notifications (
  id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
  actor_id UUID NOT NULL REFERENCES actors(id) ON DELETE CASCADE,
  type TEXT NOT NULL,
  payload JSONB NOT NULL DEFAULT '{}'::jsonb,
  read_at TIMESTAMPTZ NULL,
  created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX notifications_actor_id_read_idx ON notifications(actor_id, read_at);

CREATE TABLE reports (
  id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
  reporter_actor_id UUID NOT NULL REFERENCES actors(id) ON DELETE CASCADE,
  target_actor_id UUID NULL REFERENCES actors(id) ON DELETE SET NULL,
  target_object_uri TEXT NULL,
  reason TEXT NOT NULL,
  state TEXT NOT NULL DEFAULT 'open' CHECK (state IN ('open', 'reviewed', 'resolved', 'rejected')),
  created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX reports_state_idx ON reports(state);

CREATE TABLE domain_blocks (
  id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
  domain TEXT NOT NULL UNIQUE,
  severity TEXT NOT NULL CHECK (severity IN ('silence', 'suspend', 'reject_media', 'reject_all')),
  reason TEXT NOT NULL DEFAULT '',
  created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

