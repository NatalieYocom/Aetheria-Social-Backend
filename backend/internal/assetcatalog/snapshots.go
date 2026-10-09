package assetcatalog

import (
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/google/uuid"
)

var ErrCatalogUnavailable = errors.New("catalog is temporarily unavailable")

type SnapshotClient interface {
	Snapshots(context.Context, []string, bool) (map[string]ResolvedAsset, error)
}

type snapshotEnvelope struct {
	Data []struct {
		ID           string              `json:"id"`
		Status       string              `json:"status"`
		Revision     string              `json:"revision"`
		Content      *beebaContentDetail `json:"content"`
		DownloadPath string              `json:"download_path"`
	} `json:"data"`
	CheckedAt time.Time `json:"checked_at"`
}

func validID(value string) bool {
	id, err := uuid.Parse(value)
	return err == nil && id != uuid.Nil && id.String() == value
}

func decodeCatalogJSON(body io.Reader, out any) error {
	const maxBytes = 4 << 20
	raw, err := io.ReadAll(io.LimitReader(body, maxBytes+1))
	if err != nil || len(raw) > maxBytes {
		return ErrCatalogUnavailable
	}
	if err = json.Unmarshal(raw, out); err != nil {
		return ErrCatalogUnavailable
	}
	return nil
}

func (c *BeeBaClient) Snapshots(ctx context.Context, ids []string, includeNSFW bool) (map[string]ResolvedAsset, error) {
	if len(ids) == 0 || len(ids) > 50 {
		return nil, errBadRequest("between 1 and 50 asset IDs are required")
	}
	requested := make(map[string]bool, len(ids))
	for _, id := range ids {
		if !validID(id) {
			return nil, errBadRequest("externalId must be a UUID")
		}
		requested[id] = true
	}
	raw, err := json.Marshal(struct {
		IDs         []string `json:"ids"`
		IncludeNSFW bool     `json:"include_nsfw"`
	}{ids, includeNSFW})
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, joinURL(c.catalog.APIBaseURL, "content/snapshots"), bytes.NewReader(raw))
	if err != nil {
		return nil, ErrCatalogUnavailable
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	res, err := c.client.Do(req)
	if err != nil {
		return nil, ErrCatalogUnavailable
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return nil, ErrCatalogUnavailable
	}
	var envelope snapshotEnvelope
	if err = decodeCatalogJSON(res.Body, &envelope); err != nil {
		return nil, err
	}
	if len(envelope.Data) != len(requested) || envelope.CheckedAt.IsZero() {
		return nil, ErrCatalogUnavailable
	}
	result := make(map[string]ResolvedAsset, len(ids))
	for _, entry := range envelope.Data {
		if !requested[entry.ID] {
			return nil, ErrCatalogUnavailable
		}
		if _, exists := result[entry.ID]; exists {
			return nil, ErrCatalogUnavailable
		}
		asset := ResolvedAsset{ExternalID: entry.ID, Availability: "unavailable"}
		switch entry.Status {
		case "unavailable":
		case "available":
			item := entry.Content
			if item == nil || item.ID != entry.ID || item.Status != "published" || item.Visibility != "public" || (item.NSFW && !includeNSFW) || !validID(item.File.ID) || item.File.FileSize <= 0 {
				return nil, ErrCatalogUnavailable
			}
			hash, err := hex.DecodeString(item.File.FileHashSHA256)
			if err != nil || len(hash) != 32 {
				return nil, ErrCatalogUnavailable
			}
			if _, err = time.Parse(time.RFC3339Nano, entry.Revision); err != nil {
				return nil, ErrCatalogUnavailable
			}
			expected := "/api/v1/content/" + entry.ID + "/download?version=" + item.File.ID
			if entry.DownloadPath != expected {
				return nil, ErrCatalogUnavailable
			}
			asset = ResolvedAsset{ExternalID: entry.ID, ExternalURL: joinURL(c.catalog.BaseURL, "api/v1/content", entry.ID), ContentType: beebaCategoryToAssetType(item.Category.Slug), Title: item.Title, Description: item.Description, PreviewURL: c.safePreviewURL(*item), DownloadURL: strings.TrimRight(c.catalog.BaseURL, "/") + expected, AuthorName: item.Author.Username, NSFW: item.NSFW, Tags: beebaTagSlugs(item.Tags), Metadata: map[string]any{"source": CatalogKindBeeBa, "slug": item.Slug}, VersionID: item.File.ID, SHA256: strings.ToLower(item.File.FileHashSHA256), SizeBytes: item.File.FileSize, UnlockPassword: item.UnlockPassword, Availability: "available", Revision: entry.Revision}
		default:
			return nil, ErrCatalogUnavailable
		}
		result[entry.ID] = asset
	}
	return result, nil
}

// Media IDs are issued by BeeBa. Never forward a storage URL or arbitrary gallery URL.
func (c *BeeBaClient) safePreviewURL(item beebaContentDetail) string {
	if validID(item.PreviewImageID) {
		return joinURL(c.catalog.BaseURL, "api/v1/media", item.PreviewImageID)
	}
	return ""
}

func validCatalogURL(raw string, api bool) bool {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || (u.Scheme != "http" && u.Scheme != "https") {
		return false
	}
	if api {
		return strings.TrimRight(u.Path, "/") == "/api/v1"
	}
	return u.Path == "" || u.Path == "/"
}
