CREATE TABLE asset_catalogs (
  id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
  code TEXT NOT NULL UNIQUE,
  name TEXT NOT NULL,
  kind TEXT NOT NULL CHECK (kind IN ('beeba', 'generic')),
  base_url TEXT NOT NULL DEFAULT '',
  api_base_url TEXT NOT NULL DEFAULT '',
  enabled BOOLEAN NOT NULL DEFAULT true,
  metadata JSONB NOT NULL DEFAULT '{}'::jsonb,
  created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX asset_catalogs_enabled_idx ON asset_catalogs(enabled);
CREATE INDEX asset_catalogs_kind_idx ON asset_catalogs(kind);

CREATE TRIGGER asset_catalogs_set_updated_at
BEFORE UPDATE ON asset_catalogs
FOR EACH ROW EXECUTE FUNCTION set_updated_at();

CREATE TABLE asset_refs (
  id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
  catalog_id UUID NOT NULL REFERENCES asset_catalogs(id) ON DELETE CASCADE,
  external_id TEXT NOT NULL,
  external_url TEXT NOT NULL DEFAULT '',
  content_type TEXT NOT NULL DEFAULT 'unknown' CHECK (content_type IN ('world', 'avatar', 'prop', 'prefab', 'media', 'unknown')),
  title TEXT NOT NULL DEFAULT '',
  description TEXT NOT NULL DEFAULT '',
  preview_url TEXT NOT NULL DEFAULT '',
  download_url TEXT NOT NULL DEFAULT '',
  author_name TEXT NOT NULL DEFAULT '',
  license TEXT NOT NULL DEFAULT '',
  nsfw BOOLEAN NOT NULL DEFAULT false,
  tags TEXT[] NOT NULL DEFAULT '{}',
  metadata JSONB NOT NULL DEFAULT '{}'::jsonb,
  last_synced_at TIMESTAMPTZ NULL,
  created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  CONSTRAINT asset_refs_catalog_external_unique UNIQUE (catalog_id, external_id)
);

CREATE INDEX asset_refs_catalog_id_idx ON asset_refs(catalog_id);
CREATE INDEX asset_refs_content_type_idx ON asset_refs(content_type);
CREATE INDEX asset_refs_tags_idx ON asset_refs USING GIN(tags);

CREATE TRIGGER asset_refs_set_updated_at
BEFORE UPDATE ON asset_refs
FOR EACH ROW EXECUTE FUNCTION set_updated_at();

CREATE TABLE world_asset_refs (
  world_id UUID NOT NULL REFERENCES worlds(id) ON DELETE CASCADE,
  asset_ref_id UUID NOT NULL REFERENCES asset_refs(id) ON DELETE CASCADE,
  role TEXT NOT NULL DEFAULT 'primary' CHECK (role IN ('primary', 'dependency', 'preview', 'spawn', 'environment')),
  sort_order INT NOT NULL DEFAULT 0,
  metadata JSONB NOT NULL DEFAULT '{}'::jsonb,
  created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  PRIMARY KEY (world_id, asset_ref_id, role)
);

CREATE INDEX world_asset_refs_asset_ref_id_idx ON world_asset_refs(asset_ref_id);
CREATE INDEX world_asset_refs_role_idx ON world_asset_refs(role);
