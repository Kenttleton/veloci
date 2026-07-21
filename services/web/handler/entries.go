package handler

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/labstack/echo/v4"
	"github.com/veloci/veloci/middleware"
	"github.com/veloci/veloci/queue"
	"github.com/veloci/veloci/response"
	"github.com/veloci/veloci/store"
)

// EntriesHandler handles entry (budget line) endpoints.
type EntriesHandler struct {
	s   *store.Store
	pub *queue.Publisher
}

// NewEntriesHandler creates an EntriesHandler.
func NewEntriesHandler(s *store.Store, pub *queue.Publisher) *EntriesHandler {
	return &EntriesHandler{s: s, pub: pub}
}

// entryView is the API representation of an entry with computed budget fields.
type entryView struct {
	ID            string          `json:"id"`
	LabelID       *string         `json:"label_id"`
	LabelName     *string         `json:"label_name"`
	Name          string          `json:"name"`
	Direction     string          `json:"direction"`
	EntryType     string          `json:"entry_type"`
	Period        string          `json:"period"`
	Status        string          `json:"status"`
	Source        string          `json:"source"`
	Priority      int             `json:"priority"`
	ActualRate    float64         `json:"actual_rate"`
	ProjectedRate *float64        `json:"projected_rate"`
	DriftRate     float64         `json:"drift_rate"`
	Tag           *string         `json:"tag"`
	Conditions    json.RawMessage `json:"conditions"`
	StartDate     string          `json:"start_date"`
	EndDate       *string         `json:"end_date"`
	CreatedAt     string          `json:"created_at"`
	// Engine review metadata (null for user-created entries)
	AlertType               *string  `json:"alert_type"`
	Confidence              *float64 `json:"confidence"`
	MerchantConfidence      *float64 `json:"merchant_confidence"`
	TimingConfidence        *float64 `json:"timing_confidence"`
	AmountConfidence        *float64 `json:"amount_confidence"`
	SampleMerchants         []string `json:"sample_merchants"`
	MatchedTransactionCount *int     `json:"matched_transaction_count"`
}

func toEntryView(e store.EntryRow) entryView {
	name := entryName(e)
	period := fmt.Sprintf("%dd", e.PeriodDays)

	var actualRate float64
	if e.ActualRatePerDay != nil {
		actualRate = *e.ActualRatePerDay
	}

	var driftRate float64
	if e.SnapshotDriftPerDay != nil {
		driftRate = *e.SnapshotDriftPerDay
	}

	var tag *string
	if e.SnapshotDriftPerDay != nil {
		if *e.SnapshotDriftPerDay > 0 {
			s := "boost"
			tag = &s
		} else if *e.SnapshotDriftPerDay < 0 {
			s := "hit"
			tag = &s
		}
	}

	var endDate *string
	if e.EndDate != nil {
		s := e.EndDate.Format("2006-01-02")
		endDate = &s
	}

	sampleMerchants := e.SampleMerchants
	if sampleMerchants == nil {
		sampleMerchants = []string{}
	}

	return entryView{
		ID:                      e.ID,
		LabelID:                 e.LabelID,
		LabelName:               e.LabelName,
		Name:                    name,
		Direction:               e.Direction,
		EntryType:               e.EntryType,
		Period:                  period,
		Status:                  e.Status,
		Source:                  e.Source,
		Priority:                e.Priority,
		ActualRate:              actualRate,
		ProjectedRate:           e.ProjectedRatePerDay,
		DriftRate:               driftRate,
		Tag:                     tag,
		Conditions:              e.Conditions,
		StartDate:               e.StartDate.Format("2006-01-02"),
		EndDate:                 endDate,
		CreatedAt:               e.CreatedAt.Format("2006-01-02T15:04:05Z07:00"),
		AlertType:               e.AlertType,
		Confidence:              e.Confidence,
		MerchantConfidence:      e.MerchantConfidence,
		TimingConfidence:        e.TimingConfidence,
		AmountConfidence:        e.AmountConfidence,
		SampleMerchants:         sampleMerchants,
		MatchedTransactionCount: e.MatchedTransactionCount,
	}
}

func entryName(e store.EntryRow) string {
	if e.LabelName != nil && *e.LabelName != "" {
		return *e.LabelName
	}
	return "Unlabeled"
}

func (h *EntriesHandler) ListEntries(c echo.Context) error {
	ctx := c.Request().Context()
	entityID := middleware.EntityID(ctx)

	dateFrom := c.QueryParam("date_from")
	dateTo := c.QueryParam("date_to")
	spanDays, _ := strconv.Atoi(c.QueryParam("span_days"))
	spanMonths, _ := strconv.Atoi(c.QueryParam("span_months"))
	spanYears, _ := strconv.Atoi(c.QueryParam("span_years"))
	accountID := c.QueryParam("account_id")
	status := c.QueryParam("status")
	cursor := c.QueryParam("cursor")

	limit := 200
	if limitStr := c.QueryParam("limit"); limitStr != "" {
		if v, err := strconv.Atoi(limitStr); err == nil {
			limit = v
		}
	}
	if limit == 0 {
		limit = 50
	}

	dr := store.ResolveRange(dateFrom, dateTo, spanDays, spanMonths, spanYears)
	items, err := h.s.ListEntries(ctx, entityID, dr, accountID, status, limit+1, cursor)
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
		c := store.EncodeDateCursor(last.ID, last.StartDate)
		nextCursor = &c
	}

	views := make([]entryView, len(items))
	for i, item := range items {
		item.Conditions = h.s.EnrichConditions(ctx, entityID, item.Conditions)
		views[i] = toEntryView(item)
	}
	return c.JSON(http.StatusOK, response.Page(views, nextCursor, limit, hasMore))
}

func (h *EntriesHandler) GetEntry(c echo.Context) error {
	ctx := c.Request().Context()
	entityID := middleware.EntityID(ctx)
	id := c.Param("id")

	item, err := h.s.GetEntry(ctx, entityID, id)
	if errors.Is(err, pgx.ErrNoRows) {
		return echo.NewHTTPError(http.StatusNotFound, "not found")
	}
	if err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "internal error")
	}
	item.Conditions = h.s.EnrichConditions(ctx, entityID, item.Conditions)
	return c.JSON(http.StatusOK, response.Single(toEntryView(item)))
}

func (h *EntriesHandler) CreateEntry(c echo.Context) error {
	ctx := c.Request().Context()
	entityID := middleware.EntityID(ctx)

	var body struct {
		LabelID             *string         `json:"label_id"`
		Direction           string          `json:"direction"`
		EntryType           string          `json:"entry_type"`
		PeriodDays          int             `json:"period_days"`
		VariableMethod      *string         `json:"variable_method"`
		ProjectedRatePerDay *float64        `json:"projected_rate_per_day"`
		Conditions          json.RawMessage `json:"conditions"`
		Priority            int             `json:"priority"`
		Source              string          `json:"source"`
		ProjectTentatively  bool            `json:"project_tentatively"`
		StartDate           string          `json:"start_date"`
		EndDate             *string         `json:"end_date"`
	}
	if err := c.Bind(&body); err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, "invalid body")
	}

	startDate, err := time.Parse("2006-01-02", body.StartDate)
	if err != nil {
		return echo.NewHTTPError(http.StatusUnprocessableEntity, "invalid start_date")
	}
	var endDate *time.Time
	if body.EndDate != nil {
		t, err := time.Parse("2006-01-02", *body.EndDate)
		if err != nil {
			return echo.NewHTTPError(http.StatusUnprocessableEntity, "invalid end_date")
		}
		endDate = &t
	}

	source := body.Source
	if source == "" {
		source = "user"
	}

	item, err := h.s.CreateEntry(ctx, entityID, store.CreateEntryInput{
		LabelID:             body.LabelID,
		Direction:           body.Direction,
		EntryType:           body.EntryType,
		PeriodDays:          body.PeriodDays,
		VariableMethod:      body.VariableMethod,
		ProjectedRatePerDay: body.ProjectedRatePerDay,
		Conditions:          body.Conditions,
		Priority:            body.Priority,
		Source:              source,
		ProjectTentatively:  body.ProjectTentatively,
		StartDate:           startDate,
		EndDate:             endDate,
	})
	if err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "internal error")
	}
	return c.JSON(http.StatusOK, response.Single(toEntryView(item)))
}

func (h *EntriesHandler) UpdateEntry(c echo.Context) error {
	ctx := c.Request().Context()
	entityID := middleware.EntityID(ctx)
	id := c.Param("id")

	var body struct {
		LabelID             *string         `json:"label_id"`
		Direction           string          `json:"direction"`
		EntryType           string          `json:"entry_type"`
		PeriodDays          int             `json:"period_days"`
		VariableMethod      *string         `json:"variable_method"`
		ProjectedRatePerDay *float64        `json:"projected_rate_per_day"`
		Conditions          json.RawMessage `json:"conditions"`
		Priority            int             `json:"priority"`
		Status              string          `json:"status"`
		ProjectTentatively  bool            `json:"project_tentatively"`
		StartDate           string          `json:"start_date"`
		EndDate             *string         `json:"end_date"`
	}
	if err := c.Bind(&body); err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, "invalid body")
	}

	startDate, err := time.Parse("2006-01-02", body.StartDate)
	if err != nil {
		return echo.NewHTTPError(http.StatusUnprocessableEntity, "invalid start_date")
	}
	var endDate *time.Time
	if body.EndDate != nil {
		t, err := time.Parse("2006-01-02", *body.EndDate)
		if err != nil {
			return echo.NewHTTPError(http.StatusUnprocessableEntity, "invalid end_date")
		}
		endDate = &t
	}

	conditions := body.Conditions
	if len(conditions) > 0 {
		resolved, resolveErr := h.s.ResolveConditions(ctx, entityID, conditions)
		if resolveErr != nil {
			return echo.NewHTTPError(http.StatusUnprocessableEntity, "could not resolve conditions: "+resolveErr.Error())
		}
		conditions = resolved
	}

	item, err := h.s.UpdateEntry(ctx, entityID, id, store.UpdateEntryInput{
		LabelID:             body.LabelID,
		Direction:           body.Direction,
		EntryType:           body.EntryType,
		PeriodDays:          body.PeriodDays,
		VariableMethod:      body.VariableMethod,
		ProjectedRatePerDay: body.ProjectedRatePerDay,
		Conditions:          conditions,
		Priority:            body.Priority,
		Status:              body.Status,
		ProjectTentatively:  body.ProjectTentatively,
		StartDate:           startDate,
		EndDate:             endDate,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return echo.NewHTTPError(http.StatusNotFound, "not found")
	}
	if err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "internal error")
	}
	item.Conditions = h.s.EnrichConditions(ctx, entityID, item.Conditions)
	return c.JSON(http.StatusOK, response.Single(toEntryView(item)))
}

func (h *EntriesHandler) DeleteEntry(c echo.Context) error {
	ctx := c.Request().Context()
	entityID := middleware.EntityID(ctx)
	id := c.Param("id")

	err := h.s.DeleteEntry(ctx, entityID, id)
	if errors.Is(err, pgx.ErrNoRows) {
		return echo.NewHTTPError(http.StatusNotFound, "not found")
	}
	if err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "internal error")
	}
	return c.NoContent(http.StatusNoContent)
}

func (h *EntriesHandler) ApproveEntry(c echo.Context) error {
	ctx := c.Request().Context()
	entityID := middleware.EntityID(ctx)
	userID := middleware.UserID(ctx)
	id := c.Param("id")

	alertType, err := h.s.ApproveEntryReview(ctx, entityID, id, userID)
	if err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "internal error")
	}

	if alertType != "drift" {
		meta, _ := json.Marshal(map[string]string{})
		if job, err := h.s.CreateJob(ctx, entityID, "account.analyze", userID, meta); err == nil {
			h.pub.Publish(ctx, queue.Job{ //nolint:errcheck
				JobID:    job.ID,
				Type:     "account.analyze",
				EntityID: entityID,
				Metadata: meta,
			})
		}
	}
	return c.NoContent(http.StatusNoContent)
}

func (h *EntriesHandler) RejectEntry(c echo.Context) error {
	ctx := c.Request().Context()
	entityID := middleware.EntityID(ctx)
	userID := middleware.UserID(ctx)
	id := c.Param("id")

	if err := h.s.RejectEntryReview(ctx, entityID, id, userID); err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "internal error")
	}
	return c.NoContent(http.StatusNoContent)
}

func (h *EntriesHandler) UpdateEntryConditions(c echo.Context) error {
	ctx := c.Request().Context()
	entityID := middleware.EntityID(ctx)
	id := c.Param("id")

	var body struct {
		Conditions json.RawMessage `json:"conditions"`
	}
	if err := c.Bind(&body); err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, "invalid body")
	}

	if !json.Valid(body.Conditions) {
		return echo.NewHTTPError(http.StatusUnprocessableEntity, "conditions must be valid JSON")
	}

	resolved, resolveErr := h.s.ResolveConditions(ctx, entityID, body.Conditions)
	if resolveErr != nil {
		return echo.NewHTTPError(http.StatusUnprocessableEntity, "could not resolve conditions: "+resolveErr.Error())
	}

	item, err := h.s.UpdateEntryConditions(ctx, entityID, id, resolved)
	if errors.Is(err, pgx.ErrNoRows) {
		return echo.NewHTTPError(http.StatusNotFound, "not found")
	}
	if err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "internal error")
	}
	item.Conditions = h.s.EnrichConditions(ctx, entityID, item.Conditions)
	return c.JSON(http.StatusOK, response.Single(toEntryView(item)))
}

func (h *EntriesHandler) PreviewEntry(c echo.Context) error {
	ctx := c.Request().Context()
	entityID := middleware.EntityID(ctx)

	var body struct {
		Conditions json.RawMessage `json:"conditions"`
	}
	if err := c.Bind(&body); err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, "invalid body")
	}

	count, ids, err := h.s.PreviewConditions(ctx, entityID, body.Conditions)
	if err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "internal error")
	}
	if ids == nil {
		ids = []string{}
	}

	return c.JSON(http.StatusOK, struct {
		MatchedCount   int      `json:"matched_count"`
		TransactionIDs []string `json:"transaction_ids"`
	}{
		MatchedCount:   count,
		TransactionIDs: ids,
	})
}

// RegisterEntriesRoutes registers entry endpoints on the given Echo group.
func RegisterEntriesRoutes(g *echo.Group, s *store.Store, pub *queue.Publisher, perms middleware.PermissionCache) {
	h := NewEntriesHandler(s, pub)

	read := g.Group("", middleware.RequirePermission(perms, "accounts:read"))
	write := g.Group("", middleware.RequirePermission(perms, "entries:write"))

	read.GET("/entries", h.ListEntries)
	read.GET("/entries/:id", h.GetEntry)
	read.POST("/entries/preview", h.PreviewEntry)

	write.POST("/entries", h.CreateEntry)
	write.PUT("/entries/:id", h.UpdateEntry)
	write.DELETE("/entries/:id", h.DeleteEntry)
	write.POST("/entries/:id/approve", h.ApproveEntry)
	write.POST("/entries/:id/reject", h.RejectEntry)
	write.PATCH("/entries/:id/conditions", h.UpdateEntryConditions)
}
