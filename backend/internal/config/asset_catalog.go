package config

import (
	"errors"
	"net/url"
	"strings"
	"time"
)

func (c AssetCatalogConfig) Validate(environment string) error {
	if !c.Enabled {
		return nil
	}
	if c.Kind != "beeba" || strings.TrimSpace(c.Code) == "" {
		return errors.New("asset catalog requires a code and beeba kind")
	}
	if c.Timeout <= 0 || c.Timeout > 10*time.Second {
		return errors.New("ASSET_CATALOG_TIMEOUT must be positive and at most 10s")
	}
	if c.APIToken != "" {
		return errors.New("ASSET_CATALOG_API_TOKEN is unsupported: public catalog requests must not use operator credentials")
	}
	for i, raw := range []string{c.BaseURL, c.APIBaseURL} {
		u, err := url.Parse(raw)
		if err != nil || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
			return errors.New("asset catalog URLs must be absolute origins without credentials, query or fragment")
		}
		if u.Scheme != "https" && !(environment == "development" && u.Scheme == "http") {
			return errors.New("asset catalog requires HTTPS outside development")
		}
		path := strings.TrimRight(u.Path, "/")
		if (i == 0 && path != "") || (i == 1 && path != "/api/v1") {
			return errors.New("catalog public URL must be an origin and API URL must end in /api/v1")
		}
	}
	return nil
}
