package config

import (
	"testing"
	"time"
)

func TestAssetCatalogConfigRejectsCredentialsAndUnsafeOrigins(t *testing.T) {
	base := AssetCatalogConfig{Enabled: true, Code: "beeba", Kind: "beeba", BaseURL: "https://public.example", APIBaseURL: "https://api.example/api/v1", Timeout: time.Second}
	if err := base.Validate("production"); err != nil {
		t.Fatal(err)
	}
	tests := map[string]func(*AssetCatalogConfig){"operatorToken": func(c *AssetCatalogConfig) { c.APIToken = "operator-token" }, "publicPath": func(c *AssetCatalogConfig) { c.BaseURL += "/path" }, "internalQuery": func(c *AssetCatalogConfig) { c.APIBaseURL += "?secret=x" }, "userinfo": func(c *AssetCatalogConfig) { c.APIBaseURL = "https://user:pass@api.example/api/v1" }, "httpProduction": func(c *AssetCatalogConfig) { c.APIBaseURL = "http://api.example/api/v1" }, "noTimeout": func(c *AssetCatalogConfig) { c.Timeout = 0 }}
	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			c := base
			mutate(&c)
			if c.Validate("production") == nil {
				t.Fatal("unsafe configuration accepted")
			}
		})
	}
	base.APIBaseURL = "http://backend:8080/api/v1"
	if err := base.Validate("development"); err != nil {
		t.Fatal(err)
	}
}
