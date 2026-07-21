package handler

import (
	"encoding/json"
	"errors"
	"net/http"
	"strconv"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/labstack/echo/v4"
	"github.com/veloci/veloci/fieldregistry"
	"github.com/veloci/veloci/middleware"
	"github.com/veloci/veloci/response"
	"github.com/veloci/veloci/store"
)

// InstitutionsHandler handles institution mapping endpoints.
type InstitutionsHandler struct {
	s *store.Store
}

// NewInstitutionsHandler creates an InstitutionsHandler.
func NewInstitutionsHandler(s *store.Store) *InstitutionsHandler {
	return &InstitutionsHandler{s: s}
}

// institutionView is the API representation of an institution mapping.
type institutionView struct {
	ID                   string          `json:"id"`
	InstitutionName      string          `json:"institution_name"`
	SourceType           string          `json:"source_type"`
	SettlementWindowDays int             `json:"settlement_window_days"`
	DedupWindowDays      int             `json:"dedup_window_days"`
	AmountTolerancePct   float64         `json:"amount_tolerance_pct"`
	MappingConfig        json.RawMessage `json:"mapping_config"`
	CreatedAt            string          `json:"created_at"`
}

func toInstitutionView(i store.Institution) institutionView {
	return institutionView{
		ID:                   i.ID,
		InstitutionName:      i.InstitutionName,
		SourceType:           i.SourceType,
		SettlementWindowDays: i.SettlementWindowDays,
		DedupWindowDays:      i.DedupWindowDays,
		AmountTolerancePct:   i.AmountTolerancePct,
		MappingConfig:        i.MappingConfig,
		CreatedAt:            i.CreatedAt.Format("2006-01-02T15:04:05Z07:00"),
	}
}

// mappingConfigBody is used in create/update request bodies.
type mappingConfigBody struct {
	Layout string            `json:"layout"`
	Fields map[string]string `json:"fields"`
}

func institutionFromInput(entityID string, b struct {
	InstitutionName      string
	SourceType           string
	SettlementWindowDays *int
	DedupWindowDays      *int
	AmountTolerancePct   *float64
	MappingConfig        *mappingConfigBody
}) (store.Institution, error) {
	var cfgJSON json.RawMessage
	if b.MappingConfig != nil && b.MappingConfig.Layout != "" {
		cfg := fieldregistry.MappingConfig{
			Layout: b.MappingConfig.Layout,
			Fields: b.MappingConfig.Fields,
		}
		if err := fieldregistry.ValidateConfig(b.SourceType, cfg); err != nil {
			return store.Institution{}, err
		}
		cfgJSON, _ = json.Marshal(cfg)
	}

	settlement := 14
	if b.SettlementWindowDays != nil && *b.SettlementWindowDays != 0 {
		settlement = *b.SettlementWindowDays
	}
	dedup := 3
	if b.DedupWindowDays != nil && *b.DedupWindowDays != 0 {
		dedup = *b.DedupWindowDays
	}
	tolerance := 0.005
	if b.AmountTolerancePct != nil && *b.AmountTolerancePct != 0 {
		tolerance = *b.AmountTolerancePct
	}

	return store.Institution{
		EntityID:             entityID,
		InstitutionName:      b.InstitutionName,
		SourceType:           b.SourceType,
		SettlementWindowDays: settlement,
		DedupWindowDays:      dedup,
		AmountTolerancePct:   tolerance,
		MappingConfig:        cfgJSON,
	}, nil
}

func (h *InstitutionsHandler) GetFieldRegistry(c echo.Context) error {
	return c.JSON(http.StatusOK, fieldregistry.Registry)
}

func (h *InstitutionsHandler) ListInstitutions(c echo.Context) error {
	ctx := c.Request().Context()
	entityID := middleware.EntityID(ctx)

	items, err := h.s.ListInstitutions(ctx, entityID)
	if err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "internal error")
	}

	views := make([]institutionView, len(items))
	for i, item := range items {
		views[i] = toInstitutionView(item)
	}
	return c.JSON(http.StatusOK, response.Single(views))
}

func (h *InstitutionsHandler) GetInstitution(c echo.Context) error {
	ctx := c.Request().Context()
	entityID := middleware.EntityID(ctx)
	id := c.Param("id")

	item, err := h.s.GetInstitution(ctx, entityID, id)
	if errors.Is(err, pgx.ErrNoRows) {
		return echo.NewHTTPError(http.StatusNotFound, "not found")
	}
	if err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "internal error")
	}
	return c.JSON(http.StatusOK, response.Single(toInstitutionView(item)))
}

func (h *InstitutionsHandler) CreateInstitution(c echo.Context) error {
	ctx := c.Request().Context()
	entityID := middleware.EntityID(ctx)

	var body struct {
		InstitutionName      string             `json:"institution_name"`
		SourceType           string             `json:"source_type"`
		SettlementWindowDays *int               `json:"settlement_window_days"`
		DedupWindowDays      *int               `json:"dedup_window_days"`
		AmountTolerancePct   *float64           `json:"amount_tolerance_pct"`
		MappingConfig        *mappingConfigBody `json:"mapping_config"`
	}
	if err := c.Bind(&body); err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, "invalid body")
	}

	inst, err := institutionFromInput(entityID, struct {
		InstitutionName      string
		SourceType           string
		SettlementWindowDays *int
		DedupWindowDays      *int
		AmountTolerancePct   *float64
		MappingConfig        *mappingConfigBody
	}{
		body.InstitutionName, body.SourceType,
		body.SettlementWindowDays, body.DedupWindowDays, body.AmountTolerancePct,
		body.MappingConfig,
	})
	if err != nil {
		return echo.NewHTTPError(http.StatusUnprocessableEntity, err.Error())
	}

	item, err := h.s.CreateInstitution(ctx, entityID, inst)
	if err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "23505" {
			return echo.NewHTTPError(http.StatusConflict, "an institution with this name already exists")
		}
		return echo.NewHTTPError(http.StatusInternalServerError, "internal error")
	}
	return c.JSON(http.StatusOK, response.Single(toInstitutionView(item)))
}

func (h *InstitutionsHandler) UpdateInstitution(c echo.Context) error {
	ctx := c.Request().Context()
	entityID := middleware.EntityID(ctx)
	id := c.Param("id")

	var body struct {
		InstitutionName      string             `json:"institution_name"`
		SourceType           string             `json:"source_type"`
		SettlementWindowDays *int               `json:"settlement_window_days"`
		DedupWindowDays      *int               `json:"dedup_window_days"`
		AmountTolerancePct   *float64           `json:"amount_tolerance_pct"`
		MappingConfig        *mappingConfigBody `json:"mapping_config"`
	}
	if err := c.Bind(&body); err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, "invalid body")
	}

	inst, err := institutionFromInput(entityID, struct {
		InstitutionName      string
		SourceType           string
		SettlementWindowDays *int
		DedupWindowDays      *int
		AmountTolerancePct   *float64
		MappingConfig        *mappingConfigBody
	}{
		body.InstitutionName, body.SourceType,
		body.SettlementWindowDays, body.DedupWindowDays, body.AmountTolerancePct,
		body.MappingConfig,
	})
	if err != nil {
		return echo.NewHTTPError(http.StatusUnprocessableEntity, err.Error())
	}

	item, err := h.s.UpdateInstitution(ctx, entityID, id, inst)
	if errors.Is(err, pgx.ErrNoRows) {
		return echo.NewHTTPError(http.StatusNotFound, "not found")
	}
	if err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "internal error")
	}
	return c.JSON(http.StatusOK, response.Single(toInstitutionView(item)))
}

func (h *InstitutionsHandler) DeleteInstitution(c echo.Context) error {
	ctx := c.Request().Context()
	entityID := middleware.EntityID(ctx)
	id := c.Param("id")

	err := h.s.DeleteInstitution(ctx, entityID, id)
	if errors.Is(err, pgx.ErrNoRows) {
		return echo.NewHTTPError(http.StatusNotFound, "not found")
	}
	if err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "internal error")
	}
	return c.NoContent(http.StatusNoContent)
}

func (h *InstitutionsHandler) ListInstitutionAccounts(c echo.Context) error {
	ctx := c.Request().Context()
	entityID := middleware.EntityID(ctx)
	institutionID := c.Param("id")
	cursor := c.QueryParam("cursor")

	limit := 50
	if l, err := strconv.Atoi(c.QueryParam("limit")); err == nil && l > 0 {
		limit = l
	}

	items, err := h.s.ListAccountsByInstitution(ctx, entityID, institutionID, limit+1, cursor)
	if err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "internal error")
	}

	hasMore := len(items) > limit
	if hasMore {
		items = items[:limit]
	}
	var nextCursor *string
	if hasMore && len(items) > 0 {
		last := items[len(items)-1]
		cur := store.EncodeCursor(last.ID, last.CreatedAt)
		nextCursor = &cur
	}

	views := make([]accountView, len(items))
	for i, item := range items {
		views[i] = toAccountView(item)
	}
	return c.JSON(http.StatusOK, response.Page(views, nextCursor, limit, hasMore))
}

func (h *InstitutionsHandler) CreateInstitutionAccount(c echo.Context) error {
	ctx := c.Request().Context()
	entityID := middleware.EntityID(ctx)
	institutionID := c.Param("id")

	var body struct {
		Name             string   `json:"name"`
		AccountType      string   `json:"account_type"`
		Status           string   `json:"status"`
		InterestRate     *float64 `json:"interest_rate"`
		BalanceCents     *int64   `json:"balance_cents"`
		CreditLimitCents *int64   `json:"credit_limit_cents"`
	}
	if err := c.Bind(&body); err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, "invalid body")
	}

	item, err := h.s.CreateAccount(ctx, entityID, store.Account{
		InstitutionID:    &institutionID,
		Name:             body.Name,
		AccountType:      body.AccountType,
		Status:           body.Status,
		InterestRate:     body.InterestRate,
		BalanceCents:     body.BalanceCents,
		CreditLimitCents: body.CreditLimitCents,
	})
	if err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "internal error")
	}
	return c.JSON(http.StatusOK, response.Single(toAccountView(item)))
}

// RegisterInstitutionsRoutes registers institution endpoints on the given Echo group.
func RegisterInstitutionsRoutes(g *echo.Group, s *store.Store, perms middleware.PermissionCache) {
	h := NewInstitutionsHandler(s)

	read := g.Group("", middleware.RequirePermission(perms, "accounts:read"))
	write := g.Group("", middleware.RequirePermission(perms, "accounts:write"))

	read.GET("/field-registry", h.GetFieldRegistry)
	read.GET("/institutions", h.ListInstitutions)
	read.GET("/institutions/:id", h.GetInstitution)
	read.GET("/institutions/:id/accounts", h.ListInstitutionAccounts)

	write.POST("/institutions", h.CreateInstitution)
	write.PUT("/institutions/:id", h.UpdateInstitution)
	write.DELETE("/institutions/:id", h.DeleteInstitution)
	write.POST("/institutions/:id/accounts", h.CreateInstitutionAccount)
}
