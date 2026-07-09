package assetcatalog

import (
	"context"
	"database/sql"

	"basisvr-social-service/internal/common/dbx"

	"github.com/google/uuid"
)

const selectCatalogByCodeSQL = `
SELECT id, code, name, kind, base_url, api_base_url, enabled, metadata
FROM asset_catalogs
WHERE code = $1`

const listWorldAssetsSQL = `
SELECT ar.id, ar.catalog_id, ar.external_id, ar.external_url, ar.content_type, ar.title, ar.description,
       ar.preview_url, ar.download_url, ar.author_name, ar.license, ar.nsfw, ar.tags, ar.metadata,
       c.code, c.name, c.kind, c.base_url, c.api_base_url,
       war.role, war.sort_order, war.metadata
FROM world_asset_refs war
JOIN asset_refs ar ON ar.id = war.asset_ref_id
JOIN asset_catalogs c ON c.id = ar.catalog_id
WHERE war.world_id = $1
ORDER BY war.sort_order ASC, ar.title ASC`

type repository struct {
	db *sql.DB
}

func newRepository(db *sql.DB) repository {
	return repository{db: db}
}

func (r repository) listCatalogs(ctx context.Context) ([]Catalog, error) {
	rows, err := r.db.QueryContext(ctx, `
SELECT id, code, name, kind, base_url, api_base_url, enabled, metadata
FROM asset_catalogs
WHERE enabled = true
ORDER BY name ASC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	catalogs := []Catalog{}
	for rows.Next() {
		catalog, err := scanCatalog(rows)
		if err != nil {
			return nil, err
		}
		catalogs = append(catalogs, catalog)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return catalogs, nil
}

func (r repository) catalogByCode(ctx context.Context, code string) (Catalog, error) {
	return scanCatalog(r.db.QueryRowContext(ctx, selectCatalogByCodeSQL, code))
}

func (r repository) upsertCatalog(ctx context.Context, catalog Catalog) (Catalog, error) {
	metadata, err := dbx.MarshalJSON(catalog.Metadata, "{}")
	if err != nil {
		return Catalog{}, err
	}
	return scanCatalog(r.db.QueryRowContext(ctx, `
INSERT INTO asset_catalogs (code, name, kind, base_url, api_base_url, enabled, metadata)
VALUES ($1, $2, $3, $4, $5, $6, $7)
ON CONFLICT (code)
DO UPDATE SET name = EXCLUDED.name,
              kind = EXCLUDED.kind,
              base_url = EXCLUDED.base_url,
              api_base_url = EXCLUDED.api_base_url,
              enabled = EXCLUDED.enabled,
              metadata = asset_catalogs.metadata || EXCLUDED.metadata,
              updated_at = now()
RETURNING id, code, name, kind, base_url, api_base_url, enabled, metadata`,
		catalog.Code,
		catalog.Name,
		catalog.Kind,
		catalog.BaseURL,
		catalog.APIBaseURL,
		catalog.Enabled,
		metadata,
	))
}

func (r repository) upsertAssetRef(ctx context.Context, catalog Catalog, asset ResolvedAsset) (AssetRef, error) {
	if asset.ContentType == "" {
		asset.ContentType = AssetTypeUnknown
	}
	metadata, err := dbx.MarshalJSON(asset.Metadata, "{}")
	if err != nil {
		return AssetRef{}, err
	}
	row := r.db.QueryRowContext(ctx, `
INSERT INTO asset_refs (
  catalog_id, external_id, external_url, content_type, title, description, preview_url,
  download_url, author_name, license, nsfw, tags, metadata, last_synced_at
) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12::text[], $13, now())
ON CONFLICT (catalog_id, external_id)
DO UPDATE SET external_url = EXCLUDED.external_url,
              content_type = EXCLUDED.content_type,
              title = EXCLUDED.title,
              description = EXCLUDED.description,
              preview_url = EXCLUDED.preview_url,
              download_url = EXCLUDED.download_url,
              author_name = EXCLUDED.author_name,
              license = EXCLUDED.license,
              nsfw = EXCLUDED.nsfw,
              tags = EXCLUDED.tags,
              metadata = EXCLUDED.metadata,
              last_synced_at = now(),
              updated_at = now()
RETURNING id, catalog_id, external_id, external_url, content_type, title, description, preview_url,
          download_url, author_name, license, nsfw, tags, metadata`,
		catalog.ID,
		asset.ExternalID,
		asset.ExternalURL,
		asset.ContentType,
		asset.Title,
		asset.Description,
		asset.PreviewURL,
		asset.DownloadURL,
		asset.AuthorName,
		asset.License,
		asset.NSFW,
		dbx.PostgresTextArray(asset.Tags),
		metadata,
	)
	return scanAssetRef(row, catalog)
}

func (r repository) attachWorldAsset(ctx context.Context, worldID uuid.UUID, assetID uuid.UUID, role string, sortOrder int, metadata map[string]any) error {
	rawMetadata, err := dbx.MarshalJSON(metadata, "{}")
	if err != nil {
		return err
	}
	_, err = r.db.ExecContext(ctx, `
INSERT INTO world_asset_refs (world_id, asset_ref_id, role, sort_order, metadata)
VALUES ($1, $2, $3, $4, $5)
ON CONFLICT (world_id, asset_ref_id, role)
DO UPDATE SET sort_order = EXCLUDED.sort_order,
              metadata = EXCLUDED.metadata`,
		worldID,
		assetID,
		role,
		sortOrder,
		rawMetadata,
	)
	return err
}

func (r repository) detachWorldAsset(ctx context.Context, worldID uuid.UUID, assetID uuid.UUID) error {
	_, err := r.db.ExecContext(ctx, `
DELETE FROM world_asset_refs
WHERE world_id = $1 AND asset_ref_id = $2`, worldID, assetID)
	return err
}

func (r repository) worldOwner(ctx context.Context, worldID uuid.UUID) (uuid.UUID, error) {
	var ownerID uuid.UUID
	err := r.db.QueryRowContext(ctx, `SELECT owner_actor_id FROM worlds WHERE id = $1`, worldID).Scan(&ownerID)
	return ownerID, err
}

func (r repository) worldAccess(ctx context.Context, worldID uuid.UUID) (uuid.UUID, string, error) {
	var ownerID uuid.UUID
	var visibility string
	err := r.db.QueryRowContext(ctx, `SELECT owner_actor_id, visibility FROM worlds WHERE id = $1`, worldID).Scan(&ownerID, &visibility)
	return ownerID, visibility, err
}

func (r repository) listWorldAssets(ctx context.Context, worldID uuid.UUID) ([]WorldAsset, error) {
	rows, err := r.db.QueryContext(ctx, listWorldAssetsSQL, worldID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	assets := []WorldAsset{}
	for rows.Next() {
		asset, err := scanWorldAsset(rows)
		if err != nil {
			return nil, err
		}
		assets = append(assets, asset)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return assets, nil
}

type scanner interface {
	Scan(dest ...any) error
}

func scanCatalog(row scanner) (Catalog, error) {
	var catalog Catalog
	var metadataRaw []byte
	if err := row.Scan(
		&catalog.ID,
		&catalog.Code,
		&catalog.Name,
		&catalog.Kind,
		&catalog.BaseURL,
		&catalog.APIBaseURL,
		&catalog.Enabled,
		&metadataRaw,
	); err != nil {
		return Catalog{}, err
	}
	catalog.Metadata = dbx.DecodeJSON(metadataRaw, map[string]any{})
	return catalog, nil
}

func scanAssetRef(row scanner, catalog Catalog) (AssetRef, error) {
	var ref AssetRef
	var catalogID uuid.UUID
	var tags dbx.TextArray
	var metadataRaw []byte
	if err := row.Scan(
		&ref.ID,
		&catalogID,
		&ref.Asset.ExternalID,
		&ref.Asset.ExternalURL,
		&ref.Asset.ContentType,
		&ref.Asset.Title,
		&ref.Asset.Description,
		&ref.Asset.PreviewURL,
		&ref.Asset.DownloadURL,
		&ref.Asset.AuthorName,
		&ref.Asset.License,
		&ref.Asset.NSFW,
		&tags,
		&metadataRaw,
	); err != nil {
		return AssetRef{}, err
	}
	catalog.ID = catalogID
	ref.Catalog = catalog
	ref.Asset.Tags = []string(tags)
	ref.Asset.Metadata = dbx.DecodeJSON(metadataRaw, map[string]any{})
	return ref, nil
}

func scanWorldAsset(row scanner) (WorldAsset, error) {
	var asset WorldAsset
	var catalog Catalog
	var catalogID uuid.UUID
	var tags dbx.TextArray
	var assetMetadataRaw []byte
	var linkMetadataRaw []byte
	if err := row.Scan(
		&asset.Asset.ID,
		&catalogID,
		&asset.Asset.Asset.ExternalID,
		&asset.Asset.Asset.ExternalURL,
		&asset.Asset.Asset.ContentType,
		&asset.Asset.Asset.Title,
		&asset.Asset.Asset.Description,
		&asset.Asset.Asset.PreviewURL,
		&asset.Asset.Asset.DownloadURL,
		&asset.Asset.Asset.AuthorName,
		&asset.Asset.Asset.License,
		&asset.Asset.Asset.NSFW,
		&tags,
		&assetMetadataRaw,
		&catalog.Code,
		&catalog.Name,
		&catalog.Kind,
		&catalog.BaseURL,
		&catalog.APIBaseURL,
		&asset.Role,
		&asset.SortOrder,
		&linkMetadataRaw,
	); err != nil {
		return WorldAsset{}, err
	}
	catalog.ID = catalogID
	catalog.Enabled = true
	catalog.Metadata = map[string]any{}
	asset.Asset.Catalog = catalog
	asset.Asset.Asset.Tags = []string(tags)
	asset.Asset.Asset.Metadata = dbx.DecodeJSON(assetMetadataRaw, map[string]any{})
	asset.Metadata = dbx.DecodeJSON(linkMetadataRaw, map[string]any{})
	return asset, nil
}
