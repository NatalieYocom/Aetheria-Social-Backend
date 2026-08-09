DROP INDEX IF EXISTS groups_public_created_idx;
DROP INDEX IF EXISTS groups_owner_idx;
DROP INDEX IF EXISTS group_events_event_idx;
DROP INDEX IF EXISTS group_worlds_world_idx;
DROP TABLE IF EXISTS group_events;
DROP TABLE IF EXISTS group_worlds;
DROP TABLE IF EXISTS group_members;
ALTER TABLE groups DROP COLUMN IF EXISTS owner_actor_id;
