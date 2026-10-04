-- Downgrade deliberately revokes linked sessions before removing their trust fence.
UPDATE auth_sessions SET revoked_at=COALESCE(revoked_at,now()) WHERE beeba_identity_version IS NOT NULL;
ALTER TABLE auth_sessions DROP COLUMN beeba_identity_version;
DROP TABLE beeba_identity_links;
