package handler

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"time"

	"github.com/labstack/echo/v4"
	"github.com/veloci/veloci/middleware"
	"github.com/veloci/veloci/queue"
	"github.com/veloci/veloci/response"
	"github.com/veloci/veloci/store"
)

// formatMIME maps internal format identifiers to HTTP MIME types.
var formatMIME = map[string]string{
	"csv":  "text/csv; charset=utf-8",
	"json": "application/json",
}

// formatExt maps internal format identifiers to file extensions.
var formatExt = map[string]string{
	"csv":  ".csv",
	"json": ".json",
}

// ExportsHandler handles export artifact endpoints.
type ExportsHandler struct {
	s   *store.Store
	pub *queue.Publisher
}

// NewExportsHandler creates an ExportsHandler.
func NewExportsHandler(s *store.Store, pub *queue.Publisher) *ExportsHandler {
	return &ExportsHandler{s: s, pub: pub}
}

// exportView is the API representation of an export row (no data bytes).
type exportView struct {
	ID         string          `json:"id"`
	JobID      string          `json:"job_id"`
	CreatedBy  *string         `json:"created_by"`
	ExportType string          `json:"export_type"`
	Format     string          `json:"format"`
	Parameters json.RawMessage `json:"parameters"`
	SizeBytes  *int64          `json:"size_bytes"`
	Filename   string          `json:"filename"`
	CreatedAt  string          `json:"created_at"`
	ExpiresAt  string          `json:"expires_at"`
}

func toExportView(e store.Export) exportView {
	return exportView{
		ID:         e.ID,
		JobID:      e.JobID,
		CreatedBy:  e.CreatedBy,
		ExportType: e.ExportType,
		Format:     e.Format,
		Parameters: e.Parameters,
		SizeBytes:  e.SizeBytes,
		Filename:   e.Filename,
		CreatedAt:  e.CreatedAt.UTC().Format(time.RFC3339),
		ExpiresAt:  e.ExpiresAt.UTC().Format(time.RFC3339),
	}
}

// createExportRequest is the body for POST /api/exports.
type createExportRequest struct {
	ExportType string          `json:"export_type"`
	Format     string          `json:"format"`
	Parameters json.RawMessage `json:"parameters"`
	Filename   string          `json:"filename"`
}

// ListExports returns paginated exports for the entity.
func (h *ExportsHandler) ListExports(c echo.Context) error {
	ctx := c.Request().Context()
	entityID := middleware.EntityID(ctx)

	limit, err := strconv.Atoi(c.QueryParam("limit"))
	if err != nil || limit <= 0 || limit > 100 {
		limit = 25
	}
	cursor := c.QueryParam("cursor")

	items, err := h.s.ListExports(ctx, entityID, limit+1, cursor)
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
		c := store.EncodeCursor(last.ID, last.CreatedAt)
		nextCursor = &c
	}

	views := make([]exportView, len(items))
	for i, item := range items {
		views[i] = toExportView(item)
	}
	return c.JSON(http.StatusOK, response.Page(views, nextCursor, limit, hasMore))
}

// CreateExport enqueues an export.report job and returns the job_id.
// The engine will generate the artifact and store it in the exports table.
func (h *ExportsHandler) CreateExport(c echo.Context) error {
	ctx := c.Request().Context()
	entityID := middleware.EntityID(ctx)
	userID := middleware.UserID(ctx)

	var req createExportRequest
	if err := c.Bind(&req); err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, "invalid request body")
	}
	if req.ExportType == "" {
		req.ExportType = "report"
	}
	if req.Format == "" {
		req.Format = "csv"
	}
	if _, ok := formatMIME[req.Format]; !ok {
		return echo.NewHTTPError(http.StatusBadRequest, fmt.Sprintf("unsupported format: %s", req.Format))
	}
	if req.Parameters == nil {
		req.Parameters = json.RawMessage("{}")
	}

	// Embed all generation inputs into job metadata so the engine is self-contained.
	type jobMeta struct {
		ExportType string          `json:"export_type"`
		Format     string          `json:"format"`
		Parameters json.RawMessage `json:"parameters"`
		Filename   string          `json:"filename,omitempty"`
	}
	meta, _ := json.Marshal(jobMeta{
		ExportType: req.ExportType,
		Format:     req.Format,
		Parameters: req.Parameters,
		Filename:   req.Filename,
	})

	job, err := h.s.CreateJob(ctx, entityID, "export.report", userID, meta)
	if err != nil {
		if isUniqueViolation(err) {
			return echo.NewHTTPError(http.StatusConflict, "an export job of this type is already active")
		}
		return echo.NewHTTPError(http.StatusInternalServerError, "internal error")
	}

	h.pub.Publish(ctx, queue.Job{ //nolint:errcheck
		JobID:    job.ID,
		Type:     "export.report",
		EntityID: entityID,
		Metadata: meta,
	})

	return c.JSON(http.StatusAccepted, response.Single(map[string]string{"job_id": job.ID}))
}

// DownloadExport streams the artifact bytes for a completed export.
func (h *ExportsHandler) DownloadExport(c echo.Context) error {
	ctx := c.Request().Context()
	entityID := middleware.EntityID(ctx)
	jobID := c.Param("job_id")

	export, err := h.s.GetExportByJob(ctx, entityID, jobID)
	if err != nil {
		return echo.NewHTTPError(http.StatusNotFound, "export not found")
	}
	if export.ExpiresAt.Before(time.Now().UTC()) {
		return echo.NewHTTPError(http.StatusGone, "export has expired")
	}

	mime, ok := formatMIME[export.Format]
	if !ok {
		mime = "application/octet-stream"
	}

	c.Response().Header().Set("Content-Type", mime)
	c.Response().Header().Set("Content-Disposition",
		fmt.Sprintf(`attachment; filename="%s"`, export.Filename))
	if export.SizeBytes != nil {
		c.Response().Header().Set("Content-Length", strconv.FormatInt(*export.SizeBytes, 10))
	}

	_, err = c.Response().Write(export.Data)
	return err
}

// isUniqueViolation checks if the error is a Postgres unique constraint violation.
func isUniqueViolation(err error) bool {
	if err == nil {
		return false
	}
	msg := err.Error()
	return contains(msg, "unique") || contains(msg, "processing_jobs_one_active")
}

func contains(s, sub string) bool {
	return len(s) >= len(sub) && (s == sub || len(s) > 0 && containsStr(s, sub))
}

func containsStr(s, sub string) bool {
	for i := 0; i <= len(s)-len(sub); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}

// RegisterExportsRoutes registers export endpoints on the given Echo group.
func RegisterExportsRoutes(g *echo.Group, s *store.Store, pub *queue.Publisher, perms middleware.PermissionCache) {
	h := NewExportsHandler(s, pub)

	read := g.Group("", middleware.RequirePermission(perms, "reports:read"))
	write := g.Group("", middleware.RequirePermission(perms, "reports:write"))

	read.GET("/exports", h.ListExports)
	read.GET("/exports/:job_id/download", h.DownloadExport)
	write.POST("/exports", h.CreateExport)
}
