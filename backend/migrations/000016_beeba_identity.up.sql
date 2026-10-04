CREATE TABLE beeba_identity_links (
  issuer text NOT NULL,
  beeba_user_id uuid NOT NULL,
  user_id uuid NOT NULL UNIQUE REFERENCES users(id),
  created_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (issuer, beeba_user_id)
);
ALTER TABLE auth_sessions ADD COLUMN beeba_identity_version bigint;
ALTER TABLE auth_sessions ADD CONSTRAINT beeba_identity_version_nonnegative CHECK (beeba_identity_version >= 0);
