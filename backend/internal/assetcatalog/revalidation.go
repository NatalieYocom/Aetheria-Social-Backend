package assetcatalog

import "context"

// Runtime operator configuration overrides an old persisted catalog row.
func (h *Handler) effectiveCatalog(c Catalog) Catalog {
	if c.Code == h.cfg.Code {
		c.Enabled = c.Enabled && h.cfg.Enabled
		c.BaseURL = h.cfg.BaseURL
		c.APIBaseURL = h.cfg.APIBaseURL
		c.Kind = h.cfg.Kind
	}
	return c
}

func (h *Handler) revalidateWorldAssets(ctx context.Context, assets []WorldAsset, includeNSFW bool) ([]WorldAsset, error) {
	if len(assets) > 50 {
		return nil, errBadRequest("world has too many legacy asset links; owner must remove excess links")
	}
	groups := make(map[string][]int)
	for i := range assets {
		assets[i].Asset.Catalog = h.effectiveCatalog(assets[i].Asset.Catalog)
		c := assets[i].Asset.Catalog
		if !c.Enabled {
			assets[i] = unavailableWorldAsset(assets[i])
			continue
		}
		groups[c.ID.String()] = append(groups[c.ID.String()], i)
	}
	for _, indexes := range groups {
		c := assets[indexes[0]].Asset.Catalog
		client, err := h.clientFactory.NewClient(c)
		if err != nil {
			return nil, ErrCatalogUnavailable
		}
		batch, ok := client.(SnapshotClient)
		if !ok {
			return nil, ErrCatalogUnavailable
		}
		ids := make([]string, 0, len(indexes))
		seen := map[string]bool{}
		for _, i := range indexes {
			id := assets[i].Asset.Asset.ExternalID
			if !seen[id] {
				ids = append(ids, id)
				seen[id] = true
			}
		}
		current, err := batch.Snapshots(ctx, ids, includeNSFW)
		if err != nil {
			return nil, ErrCatalogUnavailable
		}
		for _, i := range indexes {
			asset, exists := current[assets[i].Asset.Asset.ExternalID]
			if !exists || asset.Availability != "available" {
				assets[i] = unavailableWorldAsset(assets[i])
				continue
			}
			pinned := assets[i].PinnedVersionID
			if pinned == "" || pinned != asset.VersionID {
				asset.Availability = "update_available"
				asset.VersionID = pinned
				asset.DownloadURL = ""
				asset.UnlockPassword = ""
				asset.SHA256 = ""
				asset.SizeBytes = 0
			}
			assets[i].Asset.Asset = asset
		}
	}
	return assets, nil
}

func unavailableWorldAsset(asset WorldAsset) WorldAsset {
	asset.Asset.Asset = ResolvedAsset{ExternalID: asset.Asset.Asset.ExternalID, VersionID: asset.PinnedVersionID, Availability: "unavailable"}
	asset.Metadata = map[string]any{}
	return asset
}
