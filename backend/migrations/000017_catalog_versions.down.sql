DROP INDEX world_asset_refs_one_primary_idx;
ALTER TABLE world_asset_refs DROP COLUMN pinned_version_id;
-- Duplicate legacy primary roles deliberately are not recreated.
