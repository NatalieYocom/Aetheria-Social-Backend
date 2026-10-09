package assetcatalog

import (
	"time"

	"github.com/google/uuid"
)

const (
	CatalogKindBeeBa   = "beeba"
	CatalogKindGeneric = "generic"

	AssetTypeWorld   = "world"
	AssetTypeAvatar  = "avatar"
	AssetTypeProp    = "prop"
	AssetTypePrefab  = "prefab"
	AssetTypeMedia   = "media"
	AssetTypeUnknown = "unknown"

	WorldAssetRolePrimary     = "primary"
	WorldAssetRoleDependency  = "dependency"
	WorldAssetRolePreview     = "preview"
	WorldAssetRoleSpawn       = "spawn"
	WorldAssetRoleEnvironment = "environment"
)

type Catalog struct {
	ID         uuid.UUID
	Code       string
	Name       string
	Kind       string
	BaseURL    string
	APIBaseURL string
	Enabled    bool
	Metadata   map[string]any
}

type ResolvedAsset struct {
	VersionID      string `json:"versionId"`
	SHA256         string `json:"sha256"`
	SizeBytes      int64  `json:"sizeBytes"`
	UnlockPassword string `json:"unlockPassword"`
	Availability   string `json:"availability"`
	Revision       string `json:"revision"`

	ExternalID  string
	ExternalURL string
	ContentType string
	Title       string
	Description string
	PreviewURL  string
	DownloadURL string
	AuthorName  string
	License     string
	NSFW        bool
	Tags        []string
	Metadata    map[string]any
}

type SearchQuery struct {
	Query       string
	ContentType string
	Tags        []string
	IncludeNSFW bool
	Sort        string
	Limit       int
	Cursor      string
}

type SearchPage struct {
	Items      []ResolvedAsset
	NextCursor string
}

type AssetRef struct {
	ID        uuid.UUID
	Catalog   Catalog
	Asset     ResolvedAsset
	CreatedAt time.Time
	UpdatedAt time.Time
}

type WorldAsset struct {
	PinnedVersionID string

	Asset     AssetRef
	Role      string
	SortOrder int
	Metadata  map[string]any
}

type CatalogResponse struct {
	ID         uuid.UUID      `json:"id"`
	Code       string         `json:"code"`
	Name       string         `json:"name"`
	Kind       string         `json:"kind"`
	BaseURL    string         `json:"baseUrl"`
	APIBaseURL string         `json:"apiBaseUrl"`
	Enabled    bool           `json:"enabled"`
	Metadata   map[string]any `json:"metadata"`
}

type AssetRefResponse struct {
	VersionID      string `json:"versionId"`
	SHA256         string `json:"sha256"`
	SizeBytes      int64  `json:"sizeBytes"`
	UnlockPassword string `json:"unlockPassword"`
	Availability   string `json:"availability"`
	Revision       string `json:"revision"`

	ID          uuid.UUID       `json:"id"`
	Catalog     CatalogResponse `json:"catalog"`
	ExternalID  string          `json:"externalId"`
	ExternalURL string          `json:"externalUrl"`
	ContentType string          `json:"contentType"`
	Title       string          `json:"title"`
	Description string          `json:"description"`
	PreviewURL  string          `json:"previewUrl"`
	DownloadURL string          `json:"downloadUrl"`
	AuthorName  string          `json:"authorName"`
	License     string          `json:"license"`
	NSFW        bool            `json:"nsfw"`
	Tags        []string        `json:"tags"`
	Metadata    map[string]any  `json:"metadata"`
}

type WorldAssetResponse struct {
	Asset     AssetRefResponse `json:"asset"`
	Role      string           `json:"role"`
	SortOrder int              `json:"sortOrder"`
	Metadata  map[string]any   `json:"metadata"`
}

type SearchItemResponse struct {
	VersionID      string `json:"versionId"`
	SHA256         string `json:"sha256"`
	SizeBytes      int64  `json:"sizeBytes"`
	UnlockPassword string `json:"unlockPassword"`
	Availability   string `json:"availability"`
	Revision       string `json:"revision"`

	ExternalID  string         `json:"externalId"`
	ExternalURL string         `json:"externalUrl"`
	ContentType string         `json:"contentType"`
	Title       string         `json:"title"`
	Description string         `json:"description"`
	PreviewURL  string         `json:"previewUrl"`
	DownloadURL string         `json:"downloadUrl"`
	AuthorName  string         `json:"authorName"`
	License     string         `json:"license"`
	NSFW        bool           `json:"nsfw"`
	Tags        []string       `json:"tags"`
	Metadata    map[string]any `json:"metadata"`
}

type SearchResponse struct {
	Catalog    CatalogResponse      `json:"catalog"`
	Items      []SearchItemResponse `json:"items"`
	NextCursor string               `json:"nextCursor"`
}

type CatalogStatusResponse struct {
	CatalogCode string    `json:"catalogCode"`
	Status      string    `json:"status"`
	CheckedAt   time.Time `json:"checkedAt"`
}

func catalogResponse(catalog Catalog) CatalogResponse {
	return CatalogResponse{
		ID:         catalog.ID,
		Code:       catalog.Code,
		Name:       catalog.Name,
		Kind:       catalog.Kind,
		BaseURL:    catalog.BaseURL,
		APIBaseURL: joinURL(catalog.BaseURL, "api/v1"),
		Enabled:    catalog.Enabled,
		Metadata:   nonNilMap(catalog.Metadata),
	}
}

func assetRefResponse(ref AssetRef) AssetRefResponse {
	return AssetRefResponse{
		ID:             ref.ID,
		Catalog:        catalogResponse(ref.Catalog),
		VersionID:      ref.Asset.VersionID,
		SHA256:         ref.Asset.SHA256,
		SizeBytes:      ref.Asset.SizeBytes,
		UnlockPassword: ref.Asset.UnlockPassword,
		Availability:   ref.Asset.Availability,
		Revision:       ref.Asset.Revision,
		ExternalID:     ref.Asset.ExternalID,
		ExternalURL:    ref.Asset.ExternalURL,
		ContentType:    ref.Asset.ContentType,
		Title:          ref.Asset.Title,
		Description:    ref.Asset.Description,
		PreviewURL:     ref.Asset.PreviewURL,
		DownloadURL:    ref.Asset.DownloadURL,
		AuthorName:     ref.Asset.AuthorName,
		License:        ref.Asset.License,
		NSFW:           ref.Asset.NSFW,
		Tags:           nonNilStrings(ref.Asset.Tags),
		Metadata:       nonNilMap(ref.Asset.Metadata),
	}
}

func worldAssetResponse(asset WorldAsset) WorldAssetResponse {
	return WorldAssetResponse{
		Asset:     assetRefResponse(asset.Asset),
		Role:      asset.Role,
		SortOrder: asset.SortOrder,
		Metadata:  nonNilMap(asset.Metadata),
	}
}

func searchResponse(catalog Catalog, page SearchPage) SearchResponse {
	items := make([]SearchItemResponse, 0, len(page.Items))
	for _, item := range page.Items {
		items = append(items, searchItemResponse(item))
	}
	return SearchResponse{
		Catalog:    catalogResponse(catalog),
		Items:      items,
		NextCursor: page.NextCursor,
	}
}

func searchItemResponse(asset ResolvedAsset) SearchItemResponse {
	return SearchItemResponse{
		VersionID:      asset.VersionID,
		SHA256:         asset.SHA256,
		SizeBytes:      asset.SizeBytes,
		UnlockPassword: asset.UnlockPassword,
		Availability:   asset.Availability,
		Revision:       asset.Revision,
		ExternalID:     asset.ExternalID,
		ExternalURL:    asset.ExternalURL,
		ContentType:    asset.ContentType,
		Title:          asset.Title,
		Description:    asset.Description,
		PreviewURL:     asset.PreviewURL,
		DownloadURL:    asset.DownloadURL,
		AuthorName:     asset.AuthorName,
		License:        asset.License,
		NSFW:           asset.NSFW,
		Tags:           nonNilStrings(asset.Tags),
		Metadata:       nonNilMap(asset.Metadata),
	}
}

func nonNilMap(value map[string]any) map[string]any {
	if value == nil {
		return map[string]any{}
	}
	return value
}

func nonNilStrings(value []string) []string {
	if value == nil {
		return []string{}
	}
	return value
}
