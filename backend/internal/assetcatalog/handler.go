package assetcatalog

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"basisvr-social-service/internal/auth"
	"basisvr-social-service/internal/common/httpx"
	"basisvr-social-service/internal/config"
	"basisvr-social-service/internal/privacy"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
)

type Handler struct {
	repo          repository
	cfg           config.AssetCatalogConfig
	clientFactory ClientFactory
}

func NewHandler(db *sql.DB, cfg config.AssetCatalogConfig) *Handler {
	timeout := cfg.Timeout
	if timeout <= 0 {
		timeout = 5 * time.Second
	}
	return NewHandlerWithClientFactory(db, cfg, DefaultClientFactory{
		APIToken:   cfg.APIToken,
		HTTPClient: &http.Client{Timeout: timeout},
	})
}

func NewHandlerWithClientFactory(db *sql.DB, cfg config.AssetCatalogConfig, factory ClientFactory) *Handler {
	return &Handler{
		repo:          newRepository(db),
		cfg:           normalizeConfig(cfg),
		clientFactory: factory,
	}
}

func RegisterRoutes(r chi.Router, h *Handler, authMiddleware func(http.Handler) http.Handler) {
	r.Get("/api/assets/catalogs", h.ListCatalogs)
	r.Get("/api/assets/search", h.SearchAssets)
	r.Get("/api/worlds/{id}/assets", h.ListWorldAssets)
	r.Group(func(r chi.Router) {
		r.Use(authMiddleware)
		r.Post("/api/assets/resolve", h.ResolveAsset)
		r.Post("/api/worlds/{id}/assets", h.AttachWorldAsset)
		r.Delete("/api/worlds/{id}/assets/{assetRefId}", h.DetachWorldAsset)
	})
}

type resolveAssetRequest struct {
	Catalog    string `json:"catalog"`
	ExternalID string `json:"externalId"`
}

type attachWorldAssetRequest struct {
	AssetRefID uuid.UUID      `json:"assetRefId"`
	Catalog    string         `json:"catalog"`
	ExternalID string         `json:"externalId"`
	Role       string         `json:"role"`
	SortOrder  int            `json:"sortOrder"`
	Metadata   map[string]any `json:"metadata"`
}

func (h *Handler) ListCatalogs(w http.ResponseWriter, r *http.Request) {
	if h.cfg.Enabled {
		if _, err := h.ensureConfiguredCatalog(r.Context()); err != nil {
			httpx.WriteError(w, http.StatusInternalServerError, "catalog_config_failed", err.Error())
			return
		}
	}
	catalogs, err := h.repo.listCatalogs(r.Context())
	if err != nil {
		httpx.WriteError(w, http.StatusInternalServerError, "list_catalogs_failed", err.Error())
		return
	}
	response := make([]CatalogResponse, 0, len(catalogs))
	for _, catalog := range catalogs {
		response = append(response, catalogResponse(catalog))
	}
	httpx.WriteJSON(w, http.StatusOK, response)
}

func (h *Handler) ResolveAsset(w http.ResponseWriter, r *http.Request) {
	if _, err := auth.RequirePrincipal(r.Context()); err != nil {
		httpx.WriteError(w, http.StatusUnauthorized, "unauthorized", "authentication required")
		return
	}

	var req resolveAssetRequest
	if err := httpx.DecodeJSON(r, &req); err != nil {
		httpx.WriteError(w, http.StatusBadRequest, "invalid_json", err.Error())
		return
	}

	ref, err := h.resolveAssetRef(r.Context(), req.Catalog, req.ExternalID)
	if err != nil {
		writeResolveError(w, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, assetRefResponse(ref))
}

func (h *Handler) SearchAssets(w http.ResponseWriter, r *http.Request) {
	catalog, err := h.loadCatalog(r.Context(), r.URL.Query().Get("catalog"))
	if err != nil {
		writeCatalogError(w, err, "catalog_search_failed")
		return
	}
	if !catalog.Enabled {
		httpx.WriteError(w, http.StatusNotFound, "not_found", "asset catalog is disabled")
		return
	}
	client, err := h.clientFactory.NewClient(catalog)
	if err != nil {
		httpx.WriteError(w, http.StatusBadGateway, "catalog_search_failed", err.Error())
		return
	}
	page, err := client.SearchAssets(r.Context(), parseSearchQuery(r))
	if err != nil {
		writeCatalogError(w, err, "catalog_search_failed")
		return
	}
	httpx.WriteJSON(w, http.StatusOK, searchResponse(catalog, page))
}

func (h *Handler) ListWorldAssets(w http.ResponseWriter, r *http.Request) {
	worldID, ok := parseUUIDParam(w, r, "id")
	if !ok {
		return
	}
	allowed, err := h.canViewWorld(r.Context(), worldID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			writeWorldNotFound(w)
			return
		}
		httpx.WriteError(w, http.StatusInternalServerError, "world_access_check_failed", err.Error())
		return
	}
	if !allowed {
		writeWorldNotFound(w)
		return
	}
	assets, err := h.repo.listWorldAssets(r.Context(), worldID)
	if err != nil {
		httpx.WriteError(w, http.StatusInternalServerError, "list_world_assets_failed", err.Error())
		return
	}
	writeWorldAssets(w, assets)
}

func (h *Handler) AttachWorldAsset(w http.ResponseWriter, r *http.Request) {
	principal, err := auth.RequirePrincipal(r.Context())
	if err != nil {
		httpx.WriteError(w, http.StatusUnauthorized, "unauthorized", "authentication required")
		return
	}
	worldID, ok := parseUUIDParam(w, r, "id")
	if !ok {
		return
	}
	if !h.requireWorldOwner(w, r.Context(), worldID, principal.ActorID) {
		return
	}

	var req attachWorldAssetRequest
	if err := httpx.DecodeJSON(r, &req); err != nil {
		httpx.WriteError(w, http.StatusBadRequest, "invalid_json", err.Error())
		return
	}

	assetID := req.AssetRefID
	if assetID == uuid.Nil {
		ref, err := h.resolveAssetRef(r.Context(), req.Catalog, req.ExternalID)
		if err != nil {
			writeResolveError(w, err)
			return
		}
		assetID = ref.ID
	}
	if assetID == uuid.Nil {
		httpx.WriteError(w, http.StatusBadRequest, "invalid_asset", "assetRefId or externalId is required")
		return
	}

	role := normalizeWorldAssetRole(req.Role)
	if role == "" {
		httpx.WriteError(w, http.StatusBadRequest, "invalid_role", "role must be primary, dependency, preview, spawn or environment")
		return
	}
	if err := h.repo.attachWorldAsset(r.Context(), worldID, assetID, role, req.SortOrder, req.Metadata); err != nil {
		httpx.WriteError(w, http.StatusInternalServerError, "attach_world_asset_failed", err.Error())
		return
	}
	assets, err := h.repo.listWorldAssets(r.Context(), worldID)
	if err != nil {
		httpx.WriteError(w, http.StatusInternalServerError, "list_world_assets_failed", err.Error())
		return
	}
	writeWorldAssets(w, assets)
}

func (h *Handler) DetachWorldAsset(w http.ResponseWriter, r *http.Request) {
	principal, err := auth.RequirePrincipal(r.Context())
	if err != nil {
		httpx.WriteError(w, http.StatusUnauthorized, "unauthorized", "authentication required")
		return
	}
	worldID, ok := parseUUIDParam(w, r, "id")
	if !ok {
		return
	}
	if !h.requireWorldOwner(w, r.Context(), worldID, principal.ActorID) {
		return
	}
	assetID, ok := parseUUIDParam(w, r, "assetRefId")
	if !ok {
		return
	}
	if err := h.repo.detachWorldAsset(r.Context(), worldID, assetID); err != nil {
		httpx.WriteError(w, http.StatusInternalServerError, "detach_world_asset_failed", err.Error())
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *Handler) resolveAssetRef(ctx context.Context, catalogCode string, externalID string) (AssetRef, error) {
	externalID = strings.TrimSpace(externalID)
	if externalID == "" {
		return AssetRef{}, errBadRequest("externalId is required")
	}
	catalog, err := h.loadCatalog(ctx, catalogCode)
	if err != nil {
		return AssetRef{}, err
	}
	if !catalog.Enabled {
		return AssetRef{}, errNotFound("asset catalog is disabled")
	}
	client, err := h.clientFactory.NewClient(catalog)
	if err != nil {
		return AssetRef{}, err
	}
	asset, err := client.ResolveAsset(ctx, externalID)
	if err != nil {
		return AssetRef{}, err
	}
	return h.repo.upsertAssetRef(ctx, catalog, asset)
}

func (h *Handler) loadCatalog(ctx context.Context, code string) (Catalog, error) {
	code = strings.ToLower(strings.TrimSpace(code))
	if code == "" {
		code = h.cfg.Code
	}
	if code == "" {
		return Catalog{}, errBadRequest("catalog is required")
	}

	catalog, err := h.repo.catalogByCode(ctx, code)
	if err == nil {
		return catalog, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return Catalog{}, err
	}
	if h.cfg.Enabled && strings.EqualFold(code, h.cfg.Code) {
		return h.ensureConfiguredCatalog(ctx)
	}
	return Catalog{}, errNotFound("asset catalog not found")
}

func (h *Handler) ensureConfiguredCatalog(ctx context.Context) (Catalog, error) {
	if !h.cfg.Enabled {
		return Catalog{}, errNotFound("asset catalog is not configured")
	}
	if h.cfg.Code == "" || h.cfg.Kind == "" || h.cfg.APIBaseURL == "" {
		return Catalog{}, errBadRequest("asset catalog config requires code, kind and api base url")
	}
	return h.repo.upsertCatalog(ctx, Catalog{
		Code:       h.cfg.Code,
		Name:       h.cfg.Name,
		Kind:       h.cfg.Kind,
		BaseURL:    h.cfg.BaseURL,
		APIBaseURL: h.cfg.APIBaseURL,
		Enabled:    true,
		Metadata: map[string]any{
			"configured": true,
		},
	})
}

func (h *Handler) requireWorldOwner(w http.ResponseWriter, ctx context.Context, worldID uuid.UUID, actorID uuid.UUID) bool {
	ownerID, err := h.repo.worldOwner(ctx, worldID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			httpx.WriteError(w, http.StatusNotFound, "not_found", "world not found")
			return false
		}
		httpx.WriteError(w, http.StatusInternalServerError, "load_world_failed", err.Error())
		return false
	}
	if ownerID != actorID {
		httpx.WriteError(w, http.StatusForbidden, "forbidden", "only owner can modify world assets")
		return false
	}
	return true
}

func (h *Handler) canViewWorld(ctx context.Context, worldID uuid.UUID) (bool, error) {
	ownerID, visibility, err := h.repo.worldAccess(ctx, worldID)
	if err != nil {
		return false, err
	}
	viewer := uuid.NullUUID{}
	if principal, ok := auth.PrincipalFromContext(ctx); ok {
		viewer = uuid.NullUUID{UUID: principal.ActorID, Valid: true}
	}
	return privacy.CanView(ctx, h.repo.db, privacy.ViewInput{
		OwnerActorID:  ownerID,
		ViewerActorID: viewer,
		Visibility:    visibility,
	})
}

func writeWorldNotFound(w http.ResponseWriter) {
	httpx.WriteError(w, http.StatusNotFound, "not_found", "world not found")
}

func normalizeConfig(cfg config.AssetCatalogConfig) config.AssetCatalogConfig {
	cfg.Code = strings.ToLower(strings.TrimSpace(cfg.Code))
	cfg.Name = strings.TrimSpace(cfg.Name)
	cfg.Kind = strings.ToLower(strings.TrimSpace(cfg.Kind))
	cfg.BaseURL = strings.TrimRight(strings.TrimSpace(cfg.BaseURL), "/")
	cfg.APIBaseURL = strings.TrimRight(strings.TrimSpace(cfg.APIBaseURL), "/")
	if cfg.Name == "" && cfg.Code != "" {
		cfg.Name = cfg.Code
	}
	if cfg.APIBaseURL == "" && cfg.BaseURL != "" {
		cfg.APIBaseURL = cfg.BaseURL + "/api/v1"
	}
	return cfg
}

func normalizeWorldAssetRole(role string) string {
	role = strings.ToLower(strings.TrimSpace(role))
	if role == "" {
		role = WorldAssetRolePrimary
	}
	switch role {
	case WorldAssetRolePrimary, WorldAssetRoleDependency, WorldAssetRolePreview, WorldAssetRoleSpawn, WorldAssetRoleEnvironment:
		return role
	default:
		return ""
	}
}

func parseSearchQuery(r *http.Request) SearchQuery {
	q := r.URL.Query()
	return SearchQuery{
		Query:       strings.TrimSpace(q.Get("q")),
		ContentType: strings.TrimSpace(q.Get("type")),
		Tags:        splitCSV(q.Get("tags")),
		IncludeNSFW: parseBool(q.Get("includeNsfw")) || parseBool(q.Get("include_nsfw")),
		Sort:        normalizeSearchSort(q.Get("sort")),
		Limit:       normalizeSearchLimit(q.Get("limit")),
		Cursor:      strings.TrimSpace(q.Get("cursor")),
	}
}

func splitCSV(value string) []string {
	parts := strings.Split(value, ",")
	values := make([]string, 0, len(parts))
	for _, part := range parts {
		part = strings.TrimSpace(part)
		if part != "" {
			values = append(values, part)
		}
	}
	return values
}

func parseBool(value string) bool {
	value = strings.ToLower(strings.TrimSpace(value))
	return value == "true" || value == "1" || value == "yes"
}

func normalizeSearchSort(sort string) string {
	sort = strings.ToLower(strings.TrimSpace(sort))
	switch sort {
	case "newest", "likes", "downloads":
		return sort
	default:
		return "newest"
	}
}

func normalizeSearchLimit(raw string) int {
	var limit int
	_, _ = fmt.Sscanf(strings.TrimSpace(raw), "%d", &limit)
	if limit <= 0 {
		return 24
	}
	if limit > 50 {
		return 50
	}
	return limit
}

func parseUUIDParam(w http.ResponseWriter, r *http.Request, name string) (uuid.UUID, bool) {
	id, err := uuid.Parse(chi.URLParam(r, name))
	if err != nil {
		httpx.WriteError(w, http.StatusBadRequest, "invalid_id", name+" must be a uuid")
		return uuid.Nil, false
	}
	return id, true
}

func writeWorldAssets(w http.ResponseWriter, assets []WorldAsset) {
	response := make([]WorldAssetResponse, 0, len(assets))
	for _, asset := range assets {
		response = append(response, worldAssetResponse(asset))
	}
	httpx.WriteJSON(w, http.StatusOK, response)
}

type requestError struct {
	status  int
	code    string
	message string
}

func (e requestError) Error() string {
	return e.message
}

func errBadRequest(message string) error {
	return requestError{status: http.StatusBadRequest, code: "bad_request", message: message}
}

func errNotFound(message string) error {
	return requestError{status: http.StatusNotFound, code: "not_found", message: message}
}

func writeResolveError(w http.ResponseWriter, err error) {
	writeCatalogError(w, err, "catalog_resolve_failed")
}

func writeCatalogError(w http.ResponseWriter, err error, fallbackCode string) {
	var reqErr requestError
	if errors.As(err, &reqErr) {
		httpx.WriteError(w, reqErr.status, reqErr.code, reqErr.message)
		return
	}
	if errors.Is(err, ErrAssetNotFound) {
		httpx.WriteError(w, http.StatusNotFound, "not_found", err.Error())
		return
	}
	httpx.WriteError(w, http.StatusBadGateway, fallbackCode, err.Error())
}
