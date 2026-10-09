package assetcatalog

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"path"
	"strings"
	"time"
)

type CatalogClient interface {
	ResolveAsset(ctx context.Context, externalID string) (ResolvedAsset, error)
	SearchAssets(ctx context.Context, query SearchQuery) (SearchPage, error)
}

type CatalogHealthChecker interface {
	CheckHealth(ctx context.Context) error
}

var ErrAssetNotFound = errors.New("asset not found in catalog")

type ClientFactory interface {
	NewClient(catalog Catalog) (CatalogClient, error)
}

type DefaultClientFactory struct {
	APIToken   string
	HTTPClient *http.Client
}

func (f DefaultClientFactory) NewClient(catalog Catalog) (CatalogClient, error) {
	if !validCatalogURL(catalog.BaseURL, false) || !validCatalogURL(catalog.APIBaseURL, true) {
		return nil, ErrCatalogUnavailable
	}
	switch catalog.Kind {
	case CatalogKindBeeBa:
		return NewBeeBaClient(BeeBaClientConfig{
			Catalog:    catalog,
			APIToken:   f.APIToken,
			HTTPClient: f.HTTPClient,
		}), nil
	default:
		return nil, fmt.Errorf("unsupported asset catalog kind %q", catalog.Kind)
	}
}

type BeeBaClientConfig struct {
	Catalog    Catalog
	APIToken   string
	HTTPClient *http.Client
}

type BeeBaClient struct {
	catalog Catalog
	client  *http.Client
}

func NewBeeBaClient(cfg BeeBaClientConfig) *BeeBaClient {
	client := cfg.HTTPClient
	if client == nil {
		client = &http.Client{Timeout: 5 * time.Second}
	}
	cfg.Catalog.BaseURL = strings.TrimRight(cfg.Catalog.BaseURL, "/")
	cfg.Catalog.APIBaseURL = strings.TrimRight(cfg.Catalog.APIBaseURL, "/")
	safeClient := *client
	safeClient.Jar = nil
	safeClient.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	if safeClient.Timeout <= 0 || safeClient.Timeout > 10*time.Second {
		safeClient.Timeout = 5 * time.Second
	}
	return &BeeBaClient{catalog: cfg.Catalog, client: &safeClient}
}

func (c *BeeBaClient) CheckHealth(ctx context.Context) error {
	if c.catalog.BaseURL == "" {
		return errors.New("beeba base url is not configured")
	}
	healthBase := c.catalog.BaseURL
	if c.catalog.APIBaseURL != "" {
		origin, err := url.Parse(c.catalog.APIBaseURL)
		if err != nil {
			return ErrCatalogUnavailable
		}
		origin.Path = ""
		origin.RawPath = ""
		healthBase = origin.String()
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, joinURL(healthBase, "readyz"), nil)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "application/json")
	res, err := c.client.Do(req)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	if res.StatusCode < 200 || res.StatusCode >= 300 {
		return fmt.Errorf("catalog readiness returned %s", res.Status)
	}
	var response struct {
		Status string `json:"status"`
	}
	decoder := json.NewDecoder(io.LimitReader(res.Body, 64*1024))
	if err := decoder.Decode(&response); err != nil {
		return fmt.Errorf("decode catalog readiness: %w", err)
	}
	if response.Status != "ready" {
		return fmt.Errorf("catalog readiness status is %q", response.Status)
	}
	return nil
}

func (c *BeeBaClient) ResolveAsset(ctx context.Context, externalID string) (ResolvedAsset, error) {
	assets, err := c.Snapshots(ctx, []string{externalID}, false)
	if err != nil {
		return ResolvedAsset{}, err
	}
	asset := assets[externalID]
	if asset.Availability != "available" {
		return ResolvedAsset{}, ErrAssetNotFound
	}
	return asset, nil
}

func (c *BeeBaClient) SearchAssets(ctx context.Context, query SearchQuery) (SearchPage, error) {
	if c.catalog.APIBaseURL == "" {
		return SearchPage{}, errors.New("beeba api base url is not configured")
	}

	endpoint := "content"
	if strings.TrimSpace(query.Query) != "" {
		endpoint = "search"
	}
	searchURL := joinURL(c.catalog.APIBaseURL, endpoint)
	parsed, err := url.Parse(searchURL)
	if err != nil {
		return SearchPage{}, err
	}
	values := parsed.Query()
	if strings.TrimSpace(query.Query) != "" {
		values.Set("q", strings.TrimSpace(query.Query))
	}
	if category := assetTypeToBeeBaCategory(query.ContentType); category != "" {
		values.Set("category", category)
	}
	if len(query.Tags) > 0 {
		values.Set("tags", strings.Join(cleanStrings(query.Tags), ","))
	}
	if query.IncludeNSFW {
		values.Set("include_nsfw", "true")
	}
	if strings.TrimSpace(query.Sort) != "" {
		values.Set("sort", strings.TrimSpace(query.Sort))
	}
	if query.Limit > 0 {
		values.Set("limit", fmt.Sprintf("%d", query.Limit))
	}
	if strings.TrimSpace(query.Cursor) != "" {
		values.Set("cursor", strings.TrimSpace(query.Cursor))
	}
	parsed.RawQuery = values.Encode()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, parsed.String(), nil)
	if err != nil {
		return SearchPage{}, err
	}
	req.Header.Set("Accept", "application/json")

	res, err := c.client.Do(req)
	if err != nil {
		return SearchPage{}, err
	}
	defer res.Body.Close()

	if res.StatusCode < 200 || res.StatusCode >= 300 {
		return SearchPage{}, fmt.Errorf("catalog returned %s", res.Status)
	}

	var envelope beebaSearchEnvelope
	if err := decodeCatalogJSON(res.Body, &envelope); err != nil {
		return SearchPage{}, err
	}
	items := make([]ResolvedAsset, 0, len(envelope.Data))
	if len(envelope.Data) > 50 {
		return SearchPage{}, ErrCatalogUnavailable
	}
	for _, item := range envelope.Data {
		if !validID(item.ID) || item.Status != "published" || item.Visibility != "public" || (item.NSFW && !query.IncludeNSFW) {
			continue
		}
		asset := c.searchItem(item)
		asset.Availability = "available"
		items = append(items, asset)
	}
	return SearchPage{
		Items:      items,
		NextCursor: envelope.Pagination.NextCursor,
	}, nil
}

func (c *BeeBaClient) searchItem(item beebaContentSummary) ResolvedAsset {
	previewURL := ""
	if validID(item.PreviewImageID) {
		previewURL = joinURL(c.catalog.BaseURL, "api/v1/media", item.PreviewImageID)
	}
	metadata := map[string]any{
		"source":         CatalogKindBeeBa,
		"slug":           item.Slug,
		"status":         item.Status,
		"visibility":     item.Visibility,
		"categorySlug":   item.Category.Slug,
		"categoryName":   item.Category.Name,
		"previewImageID": item.PreviewImageID,
		"likesCount":     float64(item.LikesCount),
		"downloadsCount": float64(item.DownloadsCount),
		"commentsCount":  float64(item.CommentsCount),
	}
	if item.Author.DisplayName != "" {
		metadata["authorDisplayName"] = item.Author.DisplayName
	}
	return ResolvedAsset{
		ExternalID:  item.ID,
		ExternalURL: joinURL(c.catalog.BaseURL, "api/v1/content", item.ID),
		ContentType: beebaCategoryToAssetType(item.Category.Slug),
		Title:       item.Title,
		Description: item.Description,
		PreviewURL:  previewURL,
		DownloadURL: "",
		AuthorName:  item.Author.Username,
		NSFW:        item.NSFW,
		Tags:        beebaTagSlugs(item.Tags),
		Metadata:    metadata,
	}
}

type beebaSearchEnvelope struct {
	Data       []beebaContentSummary `json:"data"`
	Pagination beebaPagination       `json:"pagination"`
}

type beebaPagination struct {
	NextCursor string `json:"next_cursor"`
}

type beebaContentSummary struct {
	ID             string      `json:"id"`
	Slug           string      `json:"slug"`
	Title          string      `json:"title"`
	Description    string      `json:"description"`
	Status         string      `json:"status"`
	Visibility     string      `json:"visibility"`
	NSFW           bool        `json:"nsfw"`
	Category       beebaTerm   `json:"category"`
	Author         beebaAuthor `json:"author"`
	Tags           []beebaTerm `json:"tags"`
	PreviewImageID string      `json:"preview_image_id"`
	LikesCount     int         `json:"likes_count"`
	DownloadsCount int         `json:"downloads_count"`
	CommentsCount  int         `json:"comments_count"`
}

type beebaContentDetail struct {
	UnlockPassword string      `json:"unlock_password"`
	ID             string      `json:"id"`
	Slug           string      `json:"slug"`
	Title          string      `json:"title"`
	Description    string      `json:"description"`
	Status         string      `json:"status"`
	Visibility     string      `json:"visibility"`
	NSFW           bool        `json:"nsfw"`
	Category       beebaTerm   `json:"category"`
	Author         beebaAuthor `json:"author"`
	Tags           []beebaTerm `json:"tags"`
	PreviewImageID string      `json:"preview_image_id"`
	File           beebaFile   `json:"file"`
}

type beebaTerm struct {
	Slug string `json:"slug"`
	Name string `json:"name"`
}

type beebaAuthor struct {
	Username    string `json:"username"`
	DisplayName string `json:"display_name"`
}

type beebaFile struct {
	ID               string `json:"id"`
	FileSize         int64  `json:"file_size"`
	FileHashSHA256   string `json:"file_hash_sha256"`
	OriginalFilename string `json:"original_filename"`
}

func beebaCategoryToAssetType(category string) string {
	switch strings.ToLower(strings.TrimSpace(category)) {
	case "worlds":
		return AssetTypeWorld
	case "avatars":
		return AssetTypeAvatar
	case "props":
		return AssetTypeProp
	case "prefabs":
		return AssetTypePrefab
	default:
		return AssetTypeUnknown
	}
}

func assetTypeToBeeBaCategory(assetType string) string {
	switch strings.ToLower(strings.TrimSpace(assetType)) {
	case AssetTypeWorld, "worlds":
		return "worlds"
	case AssetTypeAvatar, "avatars":
		return "avatars"
	case AssetTypeProp, "props":
		return "props"
	case AssetTypePrefab, "prefabs":
		return "prefabs"
	default:
		return ""
	}
}

func cleanStrings(values []string) []string {
	cleaned := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value != "" {
			cleaned = append(cleaned, value)
		}
	}
	return cleaned
}

func beebaTagSlugs(tags []beebaTerm) []string {
	values := make([]string, 0, len(tags))
	for _, tag := range tags {
		slug := strings.TrimSpace(tag.Slug)
		if slug != "" {
			values = append(values, slug)
		}
	}
	return values
}

func joinURL(base string, parts ...string) string {
	parsed, err := url.Parse(strings.TrimRight(base, "/"))
	if err != nil {
		return strings.TrimRight(base, "/")
	}
	segments := []string{strings.Trim(parsed.Path, "/")}
	for _, part := range parts {
		if strings.Trim(part, "/") != "" {
			segments = append(segments, strings.Trim(part, "/"))
		}
	}
	parsed.Path = "/" + path.Join(segments...)
	return parsed.String()
}
