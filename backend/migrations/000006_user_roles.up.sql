ALTER TABLE users
  ADD COLUMN IF NOT EXISTS role TEXT NOT NULL DEFAULT 'user'
  CHECK (role IN ('user', 'moderator', 'admin'));

CREATE INDEX IF NOT EXISTS users_role_idx ON users(role);
