package handler

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"

	"github.com/danielgtaylor/huma/v2"
	"github.com/jackc/pgx/v5"
	"github.com/veloci/api/middleware"
	"github.com/veloci/api/queue"
	"github.com/veloci/api/response"
	"github.com/veloci/api/store"
)

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

type listImportsInput struct {
	Cursor string `query:"cursor"`
	Limit  int    `query:"limit" default:"50" minimum:"1" maximum:"200"`
}

type listImportsOutput struct {
	Body response.Envelope[[]importView]
}

type getImportInput struct {
	PathID string `path:"id"`
}

type getImportOutput struct {
	Body response.Envelope[importView]
}

func (h *ImportsHandler) ListImports(ctx context.Context, input *listImportsInput) (*listImportsOutput, error) {
	entityID := middleware.EntityID(ctx)
	limit := input.Limit
	if limit == 0 {
		limit = 50
	}

	items, err := h.s.ListPendingImports(ctx, entityID, limit+1, input.Cursor)
	if err != nil {
		return nil, huma.Error500InternalServerError("internal error")
	}

	hasMore := len(items) > limit
	if hasMore {
		items = items[:limit]
	}
	var nextCursor *string
	if hasMore && len(items) > 0 {
		last := items[len(items)-1]
		c := store.EncodeCursor(last.ID, last.UploadedAt)
		nextCursor = &c
	}

	views := make([]importView, len(items))
	for i, item := range items {
		views[i] = toImportView(item)
	}
	out := &listImportsOutput{}
	out.Body = response.Page(views, nextCursor, limit, hasMore)
	return out, nil
}

func (h *ImportsHandler) GetImport(ctx context.Context, input *getImportInput) (*getImportOutput, error) {
	entityID := middleware.EntityID(ctx)

	item, err := h.s.GetPendingImport(ctx, entityID, input.PathID)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, huma.Error404NotFound("not found")
	}
	if err != nil {
		return nil, huma.Error500InternalServerError("internal error")
	}
	out := &getImportOutput{}
	out.Body = response.Single(toImportView(item))
	return out, nil
}

// UploadImport handles multipart/form-data CSV upload as a raw chi handler.
func (h *ImportsHandler) UploadImport(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	entityID := middleware.EntityID(ctx)
	userID := middleware.UserID(ctx)

	if err := r.ParseMultipartForm(32 << 20); err != nil {
		http.Error(w, `{"title":"Bad Request","status":400,"detail":"failed to parse form"}`, http.StatusBadRequest)
		return
	}

	accountID := r.FormValue("account_id")
	if accountID == "" {
		http.Error(w, `{"title":"Bad Request","status":400,"detail":"account_id required"}`, http.StatusBadRequest)
		return
	}

	file, _, err := r.FormFile("csv")
	if err != nil {
		http.Error(w, `{"title":"Bad Request","status":400,"detail":"csv file required"}`, http.StatusBadRequest)
		return
	}
	defer file.Close()

	csvBytes, err := io.ReadAll(file)
	if err != nil {
		http.Error(w, `{"title":"Internal Server Error","status":500,"detail":"internal error"}`, http.StatusInternalServerError)
		return
	}

	importID, err := h.s.CreatePendingImport(ctx, entityID, accountID, userID, csvBytes)
	if err != nil {
		http.Error(w, `{"title":"Internal Server Error","status":500,"detail":"internal error"}`, http.StatusInternalServerError)
		return
	}

	meta, _ := json.Marshal(map[string]string{"pending_import_id": importID})
	job, err := h.s.CreateJob(ctx, entityID, "import.process", userID, meta)
	if err != nil {
		if strings.Contains(err.Error(), "processing_jobs_one_active") || strings.Contains(err.Error(), "unique") {
			http.Error(w, `{"title":"Conflict","status":409,"detail":"a job of this type is already active"}`, http.StatusConflict)
			return
		}
		http.Error(w, `{"title":"Internal Server Error","status":500,"detail":"internal error"}`, http.StatusInternalServerError)
		return
	}

	if err := h.s.SetPendingImportJob(ctx, importID, job.ID); err != nil {
		http.Error(w, `{"title":"Internal Server Error","status":500,"detail":"internal error"}`, http.StatusInternalServerError)
		return
	}

	h.pub.Publish(ctx, queue.Job{ //nolint:errcheck
		JobID:    job.ID,
		Type:     "import.process",
		EntityID: entityID,
		Metadata: meta,
	})

	item, err := h.s.GetPendingImport(ctx, entityID, importID)
	if err != nil {
		http.Error(w, `{"title":"Internal Server Error","status":500,"detail":"internal error"}`, http.StatusInternalServerError)
		return
	}

	type importCreatedBody struct {
		Data importView `json:"data"`
		Meta struct{}   `json:"meta"`
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	json.NewEncoder(w).Encode(importCreatedBody{Data: toImportView(item)}) //nolint:errcheck
}

// RegisterImportsRoutes registers import endpoints on the given Huma API and chi router.
func RegisterImportsRoutes(api huma.API, s *store.Store, pub *queue.Publisher, perms middleware.PermissionCache) {
	h := NewImportsHandler(s, pub)

	huma.Register(api, huma.Operation{
		OperationID: "list-imports",
		Method:      http.MethodGet,
		Path:        "/imports",
		Summary:     "List CSV imports",
		Tags:        []string{"imports"},
		Middlewares: huma.Middlewares{middleware.RequirePermission(perms, "accounts:read")},
	}, h.ListImports)

	huma.Register(api, huma.Operation{
		OperationID: "get-import",
		Method:      http.MethodGet,
		Path:        "/imports/{id}",
		Summary:     "Get a CSV import",
		Tags:        []string{"imports"},
		Middlewares: huma.Middlewares{middleware.RequirePermission(perms, "accounts:read")},
	}, h.GetImport)
}
