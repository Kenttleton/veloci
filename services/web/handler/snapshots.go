package handler

import (
	"encoding/csv"
	"fmt"
	"net/http"
	"strconv"
	"time"

	"github.com/labstack/echo/v4"
	"github.com/veloci/veloci/middleware"
	"github.com/veloci/veloci/response"
	"github.com/veloci/veloci/store"
)

// SnapshotsHandler handles snapshot endpoints.
type SnapshotsHandler struct {
	s *store.Store
}

// NewSnapshotsHandler creates a SnapshotsHandler.
func NewSnapshotsHandler(s *store.Store) *SnapshotsHandler {
	return &SnapshotsHandler{s: s}
}

// snapshotView is the API representation of a snapshot row.
type snapshotView struct {
	ID                      string  `json:"id"`
	NodeID                  string  `json:"node_id"`
	NodeType                string  `json:"node_type"`
	SnapshotDate            string  `json:"snapshot_date"`
	ComputedAsOf            string  `json:"computed_as_of"`
	JobID                   string  `json:"job_id"`
	ActualRatePerDay        float64 `json:"actual_rate_per_day"`
	ProjectedRatePerDay     float64 `json:"projected_rate_per_day"`
	DriftPerDay             float64 `json:"drift_per_day"`
	SlopePerDay             float64 `json:"slope_per_day"`
	RSquared                float64 `json:"r_squared"`
	TransactionCount        int     `json:"transaction_count"`
	WindowDaysUsed          int     `json:"window_days_used"`
	RollingWindowTotalCents int64   `json:"rolling_window_total_cents"`
	BalanceCents            *int64  `json:"balance_cents"`
}

func toSnapshotView(s store.Snapshot) snapshotView {
	return snapshotView{
		ID:                      s.ID,
		NodeID:                  s.NodeID,
		NodeType:                s.NodeType,
		SnapshotDate:            s.SnapshotDate.Format("2006-01-02"),
		ComputedAsOf:            s.ComputedAsOf.Format("2006-01-02T15:04:05Z07:00"),
		JobID:                   s.JobID,
		ActualRatePerDay:        s.ActualRatePerDay,
		ProjectedRatePerDay:     s.ProjectedRatePerDay,
		DriftPerDay:             s.DriftPerDay,
		SlopePerDay:             s.SlopePerDay,
		RSquared:                s.RSquared,
		TransactionCount:        s.TransactionCount,
		WindowDaysUsed:          s.WindowDaysUsed,
		RollingWindowTotalCents: s.RollingWindowTotalCents,
		BalanceCents:            s.BalanceCents,
	}
}

// snapshotSummaryView is the API representation of the aggregate summary.
type snapshotSummaryView struct {
	IncomeRate      float64 `json:"income_rate"`
	SpendRate float64 `json:"spend_rate"`
	MarginRate      float64 `json:"margin_rate"`
	DriftRate       float64 `json:"drift_rate"`
}

// snapshotHistoryView represents a single history data point.
type snapshotHistoryView struct {
	Period           string   `json:"period"`
	ActualRatePerDay float64  `json:"actual_rate_per_day"`
	OpenRate         *float64 `json:"open_rate,omitempty"`
	HighRate         *float64 `json:"high_rate,omitempty"`
	LowRate          *float64 `json:"low_rate,omitempty"`
	CloseRate        *float64 `json:"close_rate,omitempty"`
}

func (h *SnapshotsHandler) ListSnapshots(c echo.Context) error {
	ctx := c.Request().Context()
	entityID := middleware.EntityID(ctx)

	limit, err := strconv.Atoi(c.QueryParam("limit"))
	if err != nil || limit <= 0 {
		limit = 50
	}
	cursor := c.QueryParam("cursor")

	items, err := h.s.ListSnapshots(ctx, entityID, limit+1, cursor)
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
		c := store.EncodeCursor(last.ID, last.SnapshotDate)
		nextCursor = &c
	}

	views := make([]snapshotView, len(items))
	for i, item := range items {
		views[i] = toSnapshotView(item)
	}
	return c.JSON(http.StatusOK, response.Page(views, nextCursor, limit, hasMore))
}

func (h *SnapshotsHandler) GetSnapshotSummary(c echo.Context) error {
	ctx := c.Request().Context()
	entityID := middleware.EntityID(ctx)

	summary, err := h.s.GetSnapshotSummary(ctx, entityID)
	if err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "internal error")
	}

	return c.JSON(http.StatusOK, response.Single(snapshotSummaryView{
		IncomeRate:      summary.IncomeRate,
		SpendRate:  summary.SpendRate,
		MarginRate: summary.IncomeRate - summary.SpendRate,
		DriftRate:       summary.DriftRate,
	}))
}

func (h *SnapshotsHandler) GetSnapshotHistory(c echo.Context) error {
	ctx := c.Request().Context()
	entityID := middleware.EntityID(ctx)
	nodeID := c.Param("node_id")

	limit, err := strconv.Atoi(c.QueryParam("limit"))
	if err != nil || limit <= 0 {
		limit = 60
	}

	before := time.Now()
	if cursor := c.QueryParam("cursor"); cursor != "" {
		t, err := time.Parse("2006-01-02", cursor)
		if err != nil {
			return echo.NewHTTPError(http.StatusUnprocessableEntity, "invalid cursor date")
		}
		before = t
	}

	granularity := c.QueryParam("granularity")
	if granularity == "" {
		granularity = "day"
	}

	items, err := h.s.GetSnapshotHistory(ctx, entityID, nodeID, before, limit, granularity)
	if err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "internal error")
	}

	views := make([]snapshotHistoryView, len(items))
	for i, item := range items {
		v := snapshotHistoryView{
			ActualRatePerDay: item.ActualRatePerDay,
			OpenRate:         item.OpenRate,
			HighRate:         item.HighRate,
			LowRate:          item.LowRate,
			CloseRate:        item.CloseRate,
		}
		if granularity == "day" {
			v.Period = item.Period.Format("2006-01-02")
		} else {
			v.Period = item.Period.Format("2006-01-02")
		}
		views[i] = v
	}

	return c.JSON(http.StatusOK, response.Single(views))
}

// snapshotReportRow is the JSON/CSV representation of one day in the report.
type snapshotReportRow struct {
	Date       string  `json:"date"`
	IncomeRate float64 `json:"income_rate"`
	SpendRate  float64 `json:"spend_rate"`
	MarginRate float64 `json:"margin_rate"`
	DriftRate  float64 `json:"drift_rate"`
}

// GetSnapshotReport returns per-day aggregate rates for the entity.
// Supports keyset pagination via ?cursor=YYYY-MM-DD and optional date range
// via ?date_from=YYYY-MM-DD&date_to=YYYY-MM-DD. Pass ?format=csv to download.
func (h *SnapshotsHandler) GetSnapshotReport(c echo.Context) error {
	ctx := c.Request().Context()
	entityID := middleware.EntityID(ctx)

	limit, err := strconv.Atoi(c.QueryParam("limit"))
	if err != nil || limit <= 0 || limit > 500 {
		limit = 60
	}

	var before, dateFrom, dateTo *time.Time
	if v := c.QueryParam("cursor"); v != "" {
		t, err := time.Parse("2006-01-02", v)
		if err != nil {
			return echo.NewHTTPError(http.StatusUnprocessableEntity, "invalid cursor date")
		}
		before = &t
	}
	if v := c.QueryParam("date_from"); v != "" {
		t, err := time.Parse("2006-01-02", v)
		if err != nil {
			return echo.NewHTTPError(http.StatusUnprocessableEntity, "invalid date_from")
		}
		dateFrom = &t
	}
	if v := c.QueryParam("date_to"); v != "" {
		t, err := time.Parse("2006-01-02", v)
		if err != nil {
			return echo.NewHTTPError(http.StatusUnprocessableEntity, "invalid date_to")
		}
		dateTo = &t
	}

	if c.QueryParam("format") == "csv" {
		// limit=0 → no LIMIT clause; streams all matching rows.
		rows, err := h.s.ListSnapshotDaySummaries(ctx, entityID, 0, nil, dateFrom, dateTo)
		if err != nil {
			return echo.NewHTTPError(http.StatusInternalServerError, "internal error")
		}
		c.Response().Header().Set("Content-Type", "text/csv; charset=utf-8")
		c.Response().Header().Set("Content-Disposition", `attachment; filename="veloci-report.csv"`)
		w := csv.NewWriter(c.Response())
		_ = w.Write([]string{"Date", "Income/mo", "Spend/mo", "Margin/mo", "Drift/mo"})
		for _, row := range rows {
			_ = w.Write([]string{
				row.SnapshotDate.Format("2006-01-02"),
				fmt.Sprintf("%.2f", row.IncomeRate/100*30.44),
				fmt.Sprintf("%.2f", row.SpendRate/100*30.44),
				fmt.Sprintf("%.2f", row.MarginRate/100*30.44),
				fmt.Sprintf("%.2f", row.DriftRate/100*30.44),
			})
		}
		w.Flush()
		return nil
	}

	items, err := h.s.ListSnapshotDaySummaries(ctx, entityID, limit+1, before, dateFrom, dateTo)
	if err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "internal error")
	}

	hasMore := len(items) > limit
	if hasMore {
		items = items[:limit]
	}
	var nextCursor *string
	if hasMore && len(items) > 0 {
		v := items[len(items)-1].SnapshotDate.Format("2006-01-02")
		nextCursor = &v
	}

	views := make([]snapshotReportRow, len(items))
	for i, item := range items {
		views[i] = snapshotReportRow{
			Date:       item.SnapshotDate.Format("2006-01-02"),
			IncomeRate: item.IncomeRate,
			SpendRate:  item.SpendRate,
			MarginRate: item.MarginRate,
			DriftRate:  item.DriftRate,
		}
	}
	return c.JSON(http.StatusOK, response.Page(views, nextCursor, limit, hasMore))
}

// RegisterSnapshotsRoutes registers snapshot endpoints on the given Echo group.
func RegisterSnapshotsRoutes(g *echo.Group, s *store.Store, perms middleware.PermissionCache) {
	h := NewSnapshotsHandler(s)

	read := g.Group("", middleware.RequirePermission(perms, "reports:read"))
	read.GET("/snapshots", h.ListSnapshots)
	read.GET("/snapshots/summary", h.GetSnapshotSummary)
	read.GET("/snapshots/:node_id/history", h.GetSnapshotHistory)
	read.GET("/snapshots/report", h.GetSnapshotReport)
}
