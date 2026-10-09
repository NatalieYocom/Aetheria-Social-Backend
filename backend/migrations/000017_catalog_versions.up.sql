ALTER TABLE world_asset_refs ADD COLUMN pinned_version_id UUID;
-- Legacy links have no trustworthy immutable version. They require explicit owner confirmation.
-- Preserve duplicate primary links as dependencies, retaining one deterministic primary.
WITH ranked AS (
 SELECT world_id,asset_ref_id,row_number() OVER(PARTITION BY world_id ORDER BY sort_order,created_at,asset_ref_id) AS n
 FROM world_asset_refs WHERE role='primary'
)
DELETE FROM world_asset_refs w USING ranked r
WHERE w.world_id=r.world_id AND w.asset_ref_id=r.asset_ref_id AND w.role='primary' AND r.n>1
 AND EXISTS(SELECT 1 FROM world_asset_refs d WHERE d.world_id=w.world_id AND d.asset_ref_id=w.asset_ref_id AND d.role='dependency');
WITH ranked AS (
 SELECT world_id,asset_ref_id,row_number() OVER(PARTITION BY world_id ORDER BY sort_order,created_at,asset_ref_id) AS n
 FROM world_asset_refs WHERE role='primary'
)
UPDATE world_asset_refs w SET role='dependency' FROM ranked r
WHERE w.world_id=r.world_id AND w.asset_ref_id=r.asset_ref_id AND w.role='primary' AND r.n>1;
CREATE UNIQUE INDEX world_asset_refs_one_primary_idx ON world_asset_refs(world_id) WHERE role='primary';
