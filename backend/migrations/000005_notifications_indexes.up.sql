CREATE INDEX IF NOT EXISTS notifications_actor_created_idx
  ON notifications(actor_id, created_at DESC);

CREATE INDEX IF NOT EXISTS notifications_actor_unread_created_idx
  ON notifications(actor_id, created_at DESC)
  WHERE read_at IS NULL;
