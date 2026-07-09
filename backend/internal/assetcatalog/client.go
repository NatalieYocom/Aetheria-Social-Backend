package assetcatalog

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
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

var ErrAssetNotFound = errors.New("asset not found in catalog")

type ClientFactory interface {
	NewClient(catalog Catalog) (CatalogClient, error)
}

type DefaultClientFactory struct {
	APIToken   string
	HTTPClient *http.Client
}

func (f DefaultClientFactory) NewClient(catalog Catalog) (CatalogClient, error) {
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
	token   string
	client  *http.Client
}

func NewBeeBaClient(cfg BeeBaClientConfig) *BeeBaClient {
	client := cfg.HTTPClient
	if client == nil {
		client = &http.Client{Timeout: 5 * time.Second}
	}
	cfg.Catalog.BaseURL = strings.TrimRight(cfg.Catalog.BaseURL, "/")
	cfg.Catalog.APIBaseURL = strings.TrimRight(cfg.Catalog.APIBaseURL, "/")
	return &BeeBaClient{catalog: cfg.Catalog, token: cfg.APIToken, client: client}
}

func (c *BeeBaClient) ResolveAsset(ctx context.Context, externalID string) (ResolvedAsset, error) {
	externalID = strings.TrimSpace(externalID)
	if externalID == "" {
		return ResolvedAsset{}, errors.New("external asset id is required")
	}
	if c.catalog.APIBaseURL == "" {
		return ResolvedAsset{}, errors.New("beeba api base url is not configured")
	}

	detailURL := joinURL(c.catalog.APIBaseURL, "content", externalID)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, detailURL, nil)
	if err != nil {
		return ResolvedAsset{}, err
	}
	req.Header.Set("Accept", "application/json")
	if c.token != "" {
		req.Header.Set("Authorization", "Bearer "+c.token)
	}

	res, err := c.client.Do(req)
	if err != nil {
		return ResolvedAsset{}, err
	}
	defer res.Body.Close()

	if res.StatusCode == http.StatusNotFound {
		return ResolvedAsset{}, ErrAssetNotFound
	}
	if res.StatusCode < 200 || res.StatusCode >= 300 {
		return ResolvedAsset{}, fmt.Errorf("catalog returned %s", res.Status)
	}

	var envelope beebaDetailEnvelope
	if err := json.NewDecoder(res.Body).Decode(&envelope); err != nil {
		return ResolvedAsset{}, err
	}
	if envelope.Data.ID == "" {
		return ResolvedAsset{}, errors.New("catalog response is missing content id")
	}

	previewURL := c.previewURL(envelope.Data)
	metadata := map[string]any{
		"source":           CatalogKindBeeBa,
		"slug":             envelope.Data.Slug,
		"status":           envelope.Data.Status,
		"visibility":       envelope.Data.Visibility,
		"categorySlug":     envelope.Data.Category.Slug,
		"categoryName":     envelope.Data.Category.Name,
		"previewImageID":   envelope.Data.PreviewImageID,
		"fileSize":         envelope.Data.File.FileSize,
		"fileHashSHA256":   envelope.Data.File.FileHashSHA256,
		"originalFilename": envelope.Data.File.OriginalFilename,
	}
	if envelope.Data.Author.DisplayName != "" {
		metadata["authorDisplayName"] = envelope.Data.Author.DisplayName
	}

	return ResolvedAsset{
		ExternalID:  envelope.Data.ID,
		ExternalURL: joinURL(c.catalog.APIBaseURL, "content", envelope.Data.ID),
		ContentType: beebaCategoryToAssetType(envelope.Data.Category.Slug),
		Title:       envelope.Data.Title,
		Description: envelope.Data.Description,
		PreviewURL:  previewURL,
		DownloadURL: joinURL(c.catalog.APIBaseURL, "content", envelope.Data.ID, "download"),
		AuthorName:  envelope.Data.Author.Username,
		NSFW:        envelope.Data.NSFW,
		Tags:        beebaTagSlugs(envelope.Data.Tags),
		Metadata:    metadata,
	}, nil
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
	if c.token != "" {
		req.Header.Set("Authorization", "Bearer "+c.token)
	}

	res, err := c.client.Do(req)
	if err != nil {
		return SearchPage{}, err
	}
	defer res.Body.Close()

	if res.StatusCode < 200 || res.StatusCode >= 300 {
		return SearchPage{}, fmt.Errorf("catalog returned %s", res.Status)
	}

	var envelope beebaSearchEnvelope
	if err := json.NewDecoder(res.Body).Decode(&envelope); err != nil {
		return SearchPage{}, err
	}
	items := make([]ResolvedAsset, 0, len(envelope.Data))
	for _, item := range envelope.Data {
		items = append(items, c.searchItem(item))
	}
	return SearchPage{
		Items:      items,
		NextCursor: envelope.Pagination.NextCursor,
	}, nil
}

func (c *BeeBaClient) previewURL(item beebaContentDetail) string {
	for _, image := range item.Gallery {
		if image.IsPrimary && strings.TrimSpace(image.URL) != "" {
			return absoluteURL(image.URL, c.catalog.BaseURL, c.catalog.APIBaseURL)
		}
	}
	for _, image := range item.Gallery {
		if strings.TrimSpace(image.URL) != "" {
			return absoluteURL(image.URL, c.catalog.BaseURL, c.catalog.APIBaseURL)
		}
	}
	if item.PreviewImageID != "" {
		return joinURL(c.catalog.APIBaseURL, "media", item.PreviewImageID)
	}
	return ""
}

func (c *BeeBaClient) searchItem(item beebaContentSummary) ResolvedAsset {
	previewURL := ""
	if item.PreviewImageID != "" {
		previewURL = joinURL(c.catalog.APIBaseURL, "media", item.PreviewImageID)
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
		ExternalURL: joinURL(c.catalog.APIBaseURL, "content", item.ID),
		ContentType: beebaCategoryToAssetType(item.Category.Slug),
		Title:       item.Title,
		Description: item.Description,
		PreviewURL:  previewURL,
		DownloadURL: joinURL(c.catalog.APIBaseURL, "content", item.ID, "download"),
		AuthorName:  item.Author.Username,
		NSFW:        item.NSFW,
		Tags:        beebaTagSlugs(item.Tags),
		Metadata:    metadata,
	}
}

type beebaDetailEnvelope struct {
	Data beebaContentDetail `json:"data"`
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
	ID             string       `json:"id"`
	Slug           string       `json:"slug"`
	Title          string       `json:"title"`
	Description    string       `json:"description"`
	Status         string       `json:"status"`
	Visibility     string       `json:"visibility"`
	NSFW           bool         `json:"nsfw"`
	Category       beebaTerm    `json:"category"`
	Author         beebaAuthor  `json:"author"`
	Tags           []beebaTerm  `json:"tags"`
	PreviewImageID string       `json:"preview_image_id"`
	Gallery        []beebaImage `json:"gallery"`
	File           beebaFile    `json:"file"`
}

type beebaTerm struct {
	Slug string `json:"slug"`
	Name string `json:"name"`
}

type beebaAuthor struct {
	Username    string `json:"username"`
	DisplayName string `json:"display_name"`
}

type beebaImage struct {
	URL       string `json:"url"`
	IsPrimary bool   `json:"is_primary"`
}

type beebaFile struct {
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

func absoluteURL(raw string, baseURL string, apiBaseURL string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	parsed, err := url.Parse(raw)
	if err == nil && parsed.IsAbs() {
		return parsed.String()
	}
	base := strings.TrimRight(baseURL, "/")
	if base == "" {
		base = strings.TrimRight(apiBaseURL, "/")
	}
	baseParsed, err := url.Parse(base)
	if err != nil {
		return raw
	}
	if strings.HasPrefix(raw, "/") {
		return baseParsed.Scheme + "://" + baseParsed.Host + raw
	}
	baseParsed.Path = path.Join(baseParsed.Path, raw)
	return baseParsed.String()
}
