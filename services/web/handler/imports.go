package handler

import (
	"bytes"
	"encoding/csv"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/labstack/echo/v4"
	"github.com/veloci/veloci/fieldregistry"
	"github.com/veloci/veloci/middleware"
	"github.com/veloci/veloci/queue"
	"github.com/veloci/veloci/response"
	"github.com/veloci/veloci/store"
)

var csvDateFormats = []string{
	"2006-01-02",
	"01/02/2006",
	"1/2/2006",
	"01/02/06",
	"1/2/06",
	"2006/01/02",
	"Jan 2, 2006",
	"January 2, 2006",
	"02-Jan-2006",
	"2-Jan-2006",
}

func parseCSVDate(s string) (time.Time, bool) {
	s = strings.TrimSpace(s)
	for _, f := range csvDateFormats {
		if t, err := time.Parse(f, s); err == nil {
			return t, true
		}
	}
	return time.Time{}, false
}

// extractDateRange parses csvBytes and returns min date, max date, row count
// using dateCol as the column name. Returns an error if no parseable dates found.
func extractDateRange(csvBytes []byte, dateCol string) (minDate, maxDate time.Time, rowCount int, err error) {
	r := csv.NewReader(bytes.NewReader(csvBytes))
	headers, err := r.Read()
	if err != nil {
		return time.Time{}, time.Time{}, 0, err
	}
	colIdx := -1
	for i, h := range headers {
		if strings.EqualFold(strings.TrimSpace(h), strings.TrimSpace(dateCol)) {
			colIdx = i
			break
		}
	}
	if colIdx < 0 {
		return time.Time{}, time.Time{}, 0, errors.New("date column not found in CSV")
	}

	initialized := false
	for {
		row, readErr := r.Read()
		if readErr == io.EOF {
			break
		}
		if readErr != nil {
			continue
		}
		if colIdx >= len(row) {
			continue
		}
		t, ok := parseCSVDate(row[colIdx])
		if !ok {
			continue
		}
		rowCount++
		if !initialized {
			minDate = t
			maxDate = t
			initialized = true
			continue
		}
		if t.Before(minDate) {
			minDate = t
		}
		if t.After(maxDate) {
			maxDate = t
		}
	}
	if !initialized {
		return time.Time{}, time.Time{}, 0, errors.New("no parseable dates found in CSV")
	}
	return minDate, maxDate, rowCount, nil
}

// ImportsHandler handles CSV import endpoints.
type ImportsHandler struct {
	s   *store.Store
	pub *queue.Publisher
}

// NewImportsHandler creates an ImportsHandler.
func NewImportsHandler(s *store.Store, pub *queue.Publisher) *ImportsHandler {
	return &ImportsHandler{s: s, pub: pub}
}

// importView is the API representation of a pending_import.
type importView struct {
	ID             string  `json:"id"`
	AccountID      string  `json:"account_id"`
	InstitutionID  *string `json:"institution_id"`
	UploadedBy     string  `json:"uploaded_by"`
	UploadedAt     string  `json:"uploaded_at"`
	DateRangeStart *string `json:"date_range_start"`
	DateRangeEnd   *string `json:"date_range_end"`
	RowCount       *int    `json:"row_count"`
	Status         string  `json:"status"`
	JobID          *string `json:"job_id"`
	Error          *string `json:"error"`
}

func toImportView(i store.PendingImport) importView {
	v := importView{
		ID:            i.ID,
		AccountID:     i.AccountID,
		InstitutionID: i.InstitutionID,
		UploadedBy:    i.UploadedBy,
		UploadedAt:    i.UploadedAt.Format("2006-01-02T15:04:05Z07:00"),
		RowCount:      i.RowCount,
		Status:        i.Status,
		JobID:         i.JobID,
		Error:         i.Error,
	}
	if i.DateRangeStart != nil {
		s := i.DateRangeStart.Format("2006-01-02")
		v.DateRangeStart = &s
	}
	if i.DateRangeEnd != nil {
		s := i.DateRangeEnd.Format("2006-01-02")
		v.DateRangeEnd = &s
	}
	return v
}

func (h *ImportsHandler) ListImports(c echo.Context) error {
	ctx := c.Request().Context()
	entityID := middleware.EntityID(ctx)

	accountID := c.QueryParam("account_id")
	cursor := c.QueryParam("cursor")
	limit := 50
	if l, err := strconv.Atoi(c.QueryParam("limit")); err == nil && l > 0 {
		limit = l
	}

	items, err := h.s.ListPendingImports(ctx, entityID, accountID, limit+1, cursor)
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
		nc := store.EncodeCursor(last.ID, last.UploadedAt)
		nextCursor = &nc
	}

	views := make([]importView, len(items))
	for i, item := range items {
		views[i] = toImportView(item)
	}
	return c.JSON(http.StatusOK, response.Page(views, nextCursor, limit, hasMore))
}

func (h *ImportsHandler) GetImport(c echo.Context) error {
	ctx := c.Request().Context()
	entityID := middleware.EntityID(ctx)

	item, err := h.s.GetPendingImport(ctx, entityID, c.Param("id"))
	if errors.Is(err, pgx.ErrNoRows) {
		return echo.NewHTTPError(http.StatusNotFound, "not found")
	}
	if err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "internal error")
	}
	return c.JSON(http.StatusOK, response.Single(toImportView(item)))
}

// UploadImport handles multipart/form-data CSV upload.
func (h *ImportsHandler) UploadImport(c echo.Context) error {
	ctx := c.Request().Context()
	entityID := middleware.EntityID(ctx)
	userID := middleware.UserID(ctx)

	if err := c.Request().ParseMultipartForm(32 << 20); err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, "failed to parse form")
	}

	accountID := c.FormValue("account_id")
	if accountID == "" {
		return echo.NewHTTPError(http.StatusBadRequest, "account_id required")
	}

	file, _, err := c.Request().FormFile("csv")
	if err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, "csv file required")
	}
	defer file.Close()

	csvBytes, err := io.ReadAll(file)
	if err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "internal error")
	}

	// Resolve the institution mapping linked to this account so we can parse dates.
	account, err := h.s.GetAccount(ctx, entityID, accountID)
	if errors.Is(err, pgx.ErrNoRows) {
		return echo.NewHTTPError(http.StatusNotFound, "account not found")
	}
	if err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "internal error")
	}
	if account.InstitutionID == nil {
		return echo.NewHTTPError(http.StatusBadRequest, "account has no institution mapping; set one before uploading")
	}

	institution, err := h.s.GetInstitution(ctx, entityID, *account.InstitutionID)
	if err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "internal error")
	}

	var mappingCfg fieldregistry.MappingConfig
	if err := json.Unmarshal(institution.MappingConfig, &mappingCfg); err != nil || mappingCfg.Fields["date"] == "" {
		return echo.NewHTTPError(http.StatusBadRequest, "institution mapping is missing date column configuration")
	}
	minDate, maxDate, rowCount, err := extractDateRange(csvBytes, mappingCfg.Fields["date"])
	if err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, "could not parse dates from CSV using the current mapping")
	}

	importID, err := h.s.CreatePendingImport(ctx, entityID, accountID, userID, account.InstitutionID, minDate, maxDate, rowCount, csvBytes)
	if err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "internal error")
	}

	meta, _ := json.Marshal(map[string]string{"pending_import_id": importID})
	job, err := h.s.CreateJob(ctx, entityID, "import.process", userID, meta)
	if err != nil {
		if strings.Contains(err.Error(), "processing_jobs_one_active") || strings.Contains(err.Error(), "unique") {
			return echo.NewHTTPError(http.StatusConflict, "a job of this type is already active")
		}
		return echo.NewHTTPError(http.StatusInternalServerError, "internal error")
	}

	if err := h.s.SetPendingImportJob(ctx, importID, job.ID); err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "internal error")
	}

	h.pub.Publish(ctx, queue.Job{ //nolint:errcheck
		JobID:    job.ID,
		Type:     "import.process",
		EntityID: entityID,
		Metadata: meta,
	})

	item, err := h.s.GetPendingImport(ctx, entityID, importID)
	if err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "internal error")
	}

	type importCreatedBody struct {
		Data importView `json:"data"`
		Meta struct{}   `json:"meta"`
	}
	return c.JSON(http.StatusCreated, importCreatedBody{Data: toImportView(item)})
}

// RegisterImportsRoutes registers import endpoints on the given Echo group.
func RegisterImportsRoutes(g *echo.Group, s *store.Store, pub *queue.Publisher, perms middleware.PermissionCache) {
	h := NewImportsHandler(s, pub)

	read := g.Group("", middleware.RequirePermission(perms, "accounts:read"))
	read.GET("/imports", h.ListImports)
	read.GET("/imports/:id", h.GetImport)

	write := g.Group("", middleware.RequirePermission(perms, "accounts:write"))
	write.POST("/imports", h.UploadImport)
}
