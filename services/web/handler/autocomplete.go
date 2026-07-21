package handler

import (
	"net/http"

	"github.com/labstack/echo/v4"
	"github.com/veloci/veloci/middleware"
	"github.com/veloci/veloci/store"
)

type AutocompleteHandler struct {
	s *store.Store
}

func NewAutocompleteHandler(s *store.Store) *AutocompleteHandler {
	return &AutocompleteHandler{s: s}
}

func (h *AutocompleteHandler) Get(c echo.Context) error {
	ctx := c.Request().Context()
	entityID := middleware.EntityID(ctx)
	data, err := h.s.ListAutocompleteData(ctx, entityID)
	if err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "internal error")
	}
	return c.JSON(http.StatusOK, data)
}

func (h *AutocompleteHandler) UnaliasedMerchants(c echo.Context) error {
	ctx := c.Request().Context()
	entityID := middleware.EntityID(ctx)
	q := c.QueryParam("q")
	items, err := h.s.ListUnaliasedTransactionMerchants(ctx, entityID, q)
	if err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "internal error")
	}
	return c.JSON(http.StatusOK, struct {
		Items []string `json:"items"`
	}{Items: items})
}

func RegisterAutocompleteRoutes(g *echo.Group, s *store.Store, perms middleware.PermissionCache) {
	h := NewAutocompleteHandler(s)

	read := g.Group("", middleware.RequirePermission(perms, "accounts:read"))
	read.GET("/autocomplete", h.Get)
	read.GET("/transactions/merchant-strings", h.UnaliasedMerchants)
}
