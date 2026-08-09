CREATE INDEX IF NOT EXISTS users_active_username_cursor_idx
  ON users(lower(username), id)
  WHERE status = 'active';

CREATE INDEX IF NOT EXISTS actors_acct_cursor_idx
  ON actors(lower(acct), id);

CREATE INDEX IF NOT EXISTS relationships_list_cursor_idx
  ON relationships(actor_id, type, state, direction, target_actor_id);

CREATE INDEX IF NOT EXISTS invites_from_created_cursor_idx
  ON invites(from_actor_id, created_at DESC, id DESC);

CREATE INDEX IF NOT EXISTS invites_to_created_cursor_idx
  ON invites(to_actor_id, created_at DESC, id DESC);

CREATE INDEX IF NOT EXISTS notifications_actor_created_id_idx
  ON notifications(actor_id, created_at DESC, id DESC);

CREATE INDEX IF NOT EXISTS notifications_actor_unread_created_id_idx
  ON notifications(actor_id, created_at DESC, id DESC)
  WHERE read_at IS NULL;

CREATE INDEX IF NOT EXISTS worlds_visibility_created_cursor_idx
  ON worlds(visibility, created_at DESC, id DESC);

CREATE INDEX IF NOT EXISTS events_visibility_start_cursor_idx
  ON events(visibility, start_time, id);

CREATE INDEX IF NOT EXISTS reports_reporter_created_cursor_idx
  ON reports(reporter_actor_id, created_at DESC, id DESC);

CREATE INDEX IF NOT EXISTS reports_state_created_cursor_idx
  ON reports(state, created_at DESC, id DESC);

CREATE INDEX IF NOT EXISTS domain_blocks_domain_cursor_idx
  ON domain_blocks(lower(domain), id);

CREATE INDEX IF NOT EXISTS instances_world_active_created_cursor_idx
  ON instances(world_id, created_at DESC, id DESC)
  WHERE status = 'active';

CREATE INDEX IF NOT EXISTS world_server_credentials_created_cursor_idx
  ON world_server_credentials(created_at DESC, id DESC);

CREATE INDEX IF NOT EXISTS instance_join_audit_created_id_cursor_idx
  ON instance_join_audit(created_at DESC, id DESC);
