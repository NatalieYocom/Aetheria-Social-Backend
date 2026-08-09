CREATE TABLE moderation_actions (
  id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
  moderator_user_id UUID NOT NULL REFERENCES users(id) ON DELETE RESTRICT,
  target_user_id UUID NOT NULL REFERENCES users(id) ON DELETE RESTRICT,
  target_actor_id UUID NOT NULL REFERENCES actors(id) ON DELETE RESTRICT,
  action TEXT NOT NULL CHECK (action IN ('suspend', 'restore')),
  reason TEXT NOT NULL,
  report_id UUID NULL REFERENCES reports(id) ON DELETE SET NULL,
  created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX moderation_actions_target_created_idx
  ON moderation_actions(target_user_id, created_at DESC, id DESC);

CREATE INDEX moderation_actions_moderator_created_idx
  ON moderation_actions(moderator_user_id, created_at DESC, id DESC);
