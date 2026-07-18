package handler

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	"github.com/danielgtaylor/huma/v2"
	"github.com/jackc/pgx/v5"
	"github.com/veloci/veloci/middleware"
	"github.com/veloci/veloci/queue"
	"github.com/veloci/veloci/response"
	"github.com/veloci/veloci/store"
)

// CanonicalMerchantsHandler handles canonical merchant endpoints.
type CanonicalMerchantsHandler struct {
	s   *store.Store
	pub *queue.Publisher
}

// NewCanonicalMerchantsHandler creates a CanonicalMerchantsHandler.
func NewCanonicalMerchantsHandler(s *store.Store, pub *queue.Publisher) *CanonicalMerchantsHandler {
	return &CanonicalMerchantsHandler{s: s, pub: pub}
}

// canonicalMerchantView is the API representation of a canonical merchant.
type canonicalMerchantView struct {
	ID         string `json:"id"`
	Name       string `json:"name"`
	Source     string `json:"source"`
	AliasCount int    `json:"alias_count"`
	CreatedAt  string `json:"created_at"`
	UpdatedAt  string `json:"updated_at"`
}

// canonicalMerchantAliasView is the API representation of a canonical merchant alias.
type canonicalMerchantAliasView struct {
	NormalizedName      string `json:"normalized_name"`
	CanonicalMerchantID string `json:"canonical_merchant_id"`
	Source              string `json:"source"`
	CreatedAt           string `json:"created_at"`
}

func toCanonicalMerchantView(m store.CanonicalMerchant, aliasCount int) canonicalMerchantView {
	return canonicalMerchantView{
		ID:         m.ID,
		Name:       m.Name,
		Source:     m.Source,
		AliasCount: aliasCount,
		CreatedAt:  m.CreatedAt.Format("2006-01-02T15:04:05Z07:00"),
		UpdatedAt:  m.UpdatedAt.Format("2006-01-02T15:04:05Z07:00"),
	}
}

func toCanonicalMerchantWithCountsView(m store.CanonicalMerchantWithCounts) canonicalMerchantView {
	return canonicalMerchantView{
		ID:         m.ID,
		Name:       m.Name,
		Source:     m.Source,
		AliasCount: m.AliasCount,
		CreatedAt:  m.CreatedAt.Format("2006-01-02T15:04:05Z07:00"),
		UpdatedAt:  m.UpdatedAt.Format("2006-01-02T15:04:05Z07:00"),
	}
}

func toCanonicalMerchantAliasView(a store.CanonicalMerchantAlias) canonicalMerchantAliasView {
	return canonicalMerchantAliasView{
		NormalizedName:      a.NormalizedName,
		CanonicalMerchantID: a.CanonicalMerchantID,
		Source:              a.Source,
		CreatedAt:           a.CreatedAt.Format("2006-01-02T15:04:05Z07:00"),
	}
}

// triggerReprocess creates an entries.reprocess job and publishes it to the queue.
// Errors are logged but not fatal — the data change has already committed.
func (h *CanonicalMerchantsHandler) triggerReprocess(ctx context.Context, entityID, userID string) {
	meta, _ := json.Marshal(map[string]string{})
	job, err := h.s.CreateJob(ctx, entityID, "entries.reprocess", userID, meta)
	if err != nil {
		// A job may already be queued/processing — that is fine.
		return
	}
	h.pub.Publish(ctx, queue.Job{ //nolint:errcheck
		JobID:    job.ID,
		Type:     "entries.reprocess",
		EntityID: entityID,
		Metadata: meta,
	})
}

// ── List ────────────────────────────────────────────────────────────────────

type listCanonicalMerchantsInput struct {
	Cursor string `query:"cursor"`
	Limit  int    `query:"limit" default:"50" minimum:"1" maximum:"200"`
}

type listCanonicalMerchantsOutput struct {
	Body response.Envelope[[]canonicalMerchantView]
}

func (h *CanonicalMerchantsHandler) ListCanonicalMerchants(ctx context.Context, input *listCanonicalMerchantsInput) (*listCanonicalMerchantsOutput, error) {
	limit := input.Limit
	if limit == 0 {
		limit = 50
	}

	items, err := h.s.ListCanonicalMerchants(ctx, limit+1, input.Cursor)
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
		c := store.EncodeCursor(last.ID, last.CreatedAt)
		nextCursor = &c
	}

	views := make([]canonicalMerchantView, len(items))
	for i, item := range items {
		views[i] = toCanonicalMerchantWithCountsView(item)
	}
	out := &listCanonicalMerchantsOutput{}
	out.Body = response.Page(views, nextCursor, limit, hasMore)
	return out, nil
}

// ── Create ───────────────────────────────────────────────────────────────────

type createCanonicalMerchantInput struct {
	Body struct {
		Name string `json:"name" required:"true"`
	}
}

type createCanonicalMerchantOutput struct {
	Body response.Envelope[canonicalMerchantView]
}

func (h *CanonicalMerchantsHandler) CreateCanonicalMerchant(ctx context.Context, input *createCanonicalMerchantInput) (*createCanonicalMerchantOutput, error) {
	item, err := h.s.CreateCanonicalMerchant(ctx, input.Body.Name)
	if err != nil {
		if strings.Contains(err.Error(), "unique") || strings.Contains(err.Error(), "duplicate") {
			return nil, huma.Error409Conflict("canonical merchant name already exists")
		}
		return nil, huma.Error500InternalServerError("internal error")
	}
	out := &createCanonicalMerchantOutput{}
	out.Body = response.Single(toCanonicalMerchantView(item, 0))
	return out, nil
}

// ── Get ──────────────────────────────────────────────────────────────────────

type getCanonicalMerchantInput struct {
	PathID string `path:"id"`
}

type getCanonicalMerchantOutput struct {
	Body response.Envelope[canonicalMerchantView]
}

func (h *CanonicalMerchantsHandler) GetCanonicalMerchant(ctx context.Context, input *getCanonicalMerchantInput) (*getCanonicalMerchantOutput, error) {
	item, err := h.s.GetCanonicalMerchant(ctx, input.PathID)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, huma.Error404NotFound("not found")
	}
	if err != nil {
		return nil, huma.Error500InternalServerError("internal error")
	}
	out := &getCanonicalMerchantOutput{}
	out.Body = response.Single(toCanonicalMerchantView(item, 0))
	return out, nil
}

// ── Rename ───────────────────────────────────────────────────────────────────

type renameCanonicalMerchantInput struct {
	PathID string `path:"id"`
	Body   struct {
		Name string `json:"name" required:"true"`
	}
}

type renameCanonicalMerchantOutput struct {
	Body response.Envelope[canonicalMerchantView]
}

func (h *CanonicalMerchantsHandler) RenameCanonicalMerchant(ctx context.Context, input *renameCanonicalMerchantInput) (*renameCanonicalMerchantOutput, error) {
	item, err := h.s.RenameCanonicalMerchant(ctx, input.PathID, input.Body.Name)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, huma.Error404NotFound("not found")
	}
	if err != nil {
		if strings.Contains(err.Error(), "unique") || strings.Contains(err.Error(), "duplicate") {
			return nil, huma.Error409Conflict("canonical merchant name already exists")
		}
		return nil, huma.Error500InternalServerError("internal error")
	}
	out := &renameCanonicalMerchantOutput{}
	out.Body = response.Single(toCanonicalMerchantView(item, 0))
	return out, nil
}

// ── Delete ───────────────────────────────────────────────────────────────────

type deleteCanonicalMerchantInput struct {
	PathID string `path:"id"`
}

func (h *CanonicalMerchantsHandler) DeleteCanonicalMerchant(ctx context.Context, input *deleteCanonicalMerchantInput) (*struct{}, error) {
	err := h.s.DeleteCanonicalMerchant(ctx, input.PathID)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, huma.Error404NotFound("not found")
	}
	if err != nil {
		return nil, huma.Error500InternalServerError("internal error")
	}
	return nil, nil
}

// ── List aliases ─────────────────────────────────────────────────────────────

type listCanonicalMerchantAliasesInput struct {
	PathID string `path:"id"`
}

type listCanonicalMerchantAliasesOutput struct {
	Body response.Envelope[[]canonicalMerchantAliasView]
}

func (h *CanonicalMerchantsHandler) ListCanonicalMerchantAliases(ctx context.Context, input *listCanonicalMerchantAliasesInput) (*listCanonicalMerchantAliasesOutput, error) {
	items, err := h.s.ListCanonicalMerchantAliases(ctx, input.PathID)
	if err != nil {
		return nil, huma.Error500InternalServerError("internal error")
	}
	views := make([]canonicalMerchantAliasView, len(items))
	for i, item := range items {
		views[i] = toCanonicalMerchantAliasView(item)
	}
	out := &listCanonicalMerchantAliasesOutput{}
	out.Body = response.Single(views)
	return out, nil
}

// ── Add alias ────────────────────────────────────────────────────────────────

type addCanonicalMerchantAliasInput struct {
	PathID string `path:"id"`
	Body   struct {
		NormalizedName string `json:"normalized_name" required:"true"`
	}
}

type addCanonicalMerchantAliasOutput struct {
	Body response.Envelope[canonicalMerchantAliasView]
}

func (h *CanonicalMerchantsHandler) AddCanonicalMerchantAlias(ctx context.Context, input *addCanonicalMerchantAliasInput) (*addCanonicalMerchantAliasOutput, error) {
	item, err := h.s.AddCanonicalMerchantAlias(ctx, input.PathID, input.Body.NormalizedName)
	if err != nil {
		if strings.Contains(err.Error(), "unique") || strings.Contains(err.Error(), "duplicate") {
			return nil, huma.Error409Conflict("alias already exists")
		}
		if strings.Contains(err.Error(), "foreign key") || strings.Contains(err.Error(), "fkey") {
			return nil, huma.Error404NotFound("canonical merchant not found")
		}
		return nil, huma.Error500InternalServerError("internal error")
	}
	out := &addCanonicalMerchantAliasOutput{}
	out.Body = response.Single(toCanonicalMerchantAliasView(item))
	return out, nil
}

// ── Delete alias ─────────────────────────────────────────────────────────────

type deleteCanonicalMerchantAliasInput struct {
	PathID         string `path:"id"`
	NormalizedName string `path:"normalized_name"`
}

func (h *CanonicalMerchantsHandler) DeleteCanonicalMerchantAlias(ctx context.Context, input *deleteCanonicalMerchantAliasInput) (*struct{}, error) {
	err := h.s.DeleteCanonicalMerchantAlias(ctx, input.NormalizedName)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, huma.Error404NotFound("not found")
	}
	if err != nil {
		return nil, huma.Error500InternalServerError("internal error")
	}
	return nil, nil
}

// ── Merge ────────────────────────────────────────────────────────────────────

type mergeCanonicalMerchantInput struct {
	PathID string `path:"id"`
	Body   struct {
		// AbsorbID is the canonical merchant that will be absorbed (deleted).
		// The merchant identified by {id} in the path is the survivor.
		AbsorbID string `json:"absorb_id" required:"true"`
	}
}

type mergeCanonicalMerchantOutput struct {
	Body response.Envelope[canonicalMerchantView]
}

func (h *CanonicalMerchantsHandler) MergeCanonicalMerchant(ctx context.Context, input *mergeCanonicalMerchantInput) (*mergeCanonicalMerchantOutput, error) {
	survivorID := input.PathID
	absorbedID := input.Body.AbsorbID

	if survivorID == absorbedID {
		return nil, huma.Error422UnprocessableEntity("cannot merge a merchant into itself")
	}

	if err := h.s.MergeCanonicalMerchants(ctx, survivorID, absorbedID); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, huma.Error404NotFound("not found")
		}
		return nil, huma.Error500InternalServerError("internal error")
	}

	// Trigger reprocess so entry conditions referencing the absorbed UUID get reprocessed.
	entityID := middleware.EntityID(ctx)
	userID := middleware.UserID(ctx)
	h.triggerReprocess(ctx, entityID, userID)

	survivor, err := h.s.GetCanonicalMerchant(ctx, survivorID)
	if err != nil {
		return nil, huma.Error500InternalServerError("internal error")
	}
	out := &mergeCanonicalMerchantOutput{}
	out.Body = response.Single(toCanonicalMerchantView(survivor, 0))
	return out, nil
}

// ── Split ────────────────────────────────────────────────────────────────────

type splitCanonicalMerchantInput struct {
	PathID string `path:"id"`
	Body   struct {
		// NewName is the name for the newly created canonical merchant.
		NewName string `json:"new_name" required:"true"`
		// Aliases is the list of normalized_name values to move to the new merchant.
		Aliases []string `json:"aliases" required:"true"`
	}
}

type splitCanonicalMerchantOutput struct {
	Body response.Envelope[canonicalMerchantView]
}

func (h *CanonicalMerchantsHandler) SplitCanonicalMerchant(ctx context.Context, input *splitCanonicalMerchantInput) (*splitCanonicalMerchantOutput, error) {
	if len(input.Body.Aliases) == 0 {
		return nil, huma.Error422UnprocessableEntity("aliases must not be empty")
	}

	newMerchant, err := h.s.SplitCanonicalMerchant(ctx, input.PathID, input.Body.Aliases, input.Body.NewName)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, huma.Error404NotFound("not found")
	}
	if err != nil {
		if strings.Contains(err.Error(), "unique") || strings.Contains(err.Error(), "duplicate") {
			return nil, huma.Error409Conflict("canonical merchant name already exists")
		}
		return nil, huma.Error500InternalServerError("internal error")
	}

	// Trigger reprocess so engine re-evaluates entries with the updated alias mapping.
	entityID := middleware.EntityID(ctx)
	userID := middleware.UserID(ctx)
	h.triggerReprocess(ctx, entityID, userID)

	out := &splitCanonicalMerchantOutput{}
	out.Body = response.Single(toCanonicalMerchantView(newMerchant, len(input.Body.Aliases)))
	return out, nil
}

// RegisterCanonicalMerchantsRoutes registers canonical merchant endpoints on the given Huma API.
func RegisterCanonicalMerchantsRoutes(api huma.API, s *store.Store, pub *queue.Publisher, perms middleware.PermissionCache) {
	h := NewCanonicalMerchantsHandler(s, pub)

	huma.Register(api, huma.Operation{
		OperationID: "list-canonical-merchants",
		Method:      http.MethodGet,
		Path:        "/canonical-merchants",
		Summary:     "List all canonical merchants",
		Tags:        []string{"canonical-merchants"},
		Middlewares: huma.Middlewares{middleware.RequirePermission(perms, "accounts:read")},
	}, h.ListCanonicalMerchants)

	huma.Register(api, huma.Operation{
		OperationID: "create-canonical-merchant",
		Method:      http.MethodPost,
		Path:        "/canonical-merchants",
		Summary:     "Create a canonical merchant",
		Tags:        []string{"canonical-merchants"},
		Middlewares: huma.Middlewares{middleware.RequirePermission(perms, "labels:write")},
	}, h.CreateCanonicalMerchant)

	huma.Register(api, huma.Operation{
		OperationID: "get-canonical-merchant",
		Method:      http.MethodGet,
		Path:        "/canonical-merchants/{id}",
		Summary:     "Get a canonical merchant",
		Tags:        []string{"canonical-merchants"},
		Middlewares: huma.Middlewares{middleware.RequirePermission(perms, "accounts:read")},
	}, h.GetCanonicalMerchant)

	huma.Register(api, huma.Operation{
		OperationID: "rename-canonical-merchant",
		Method:      http.MethodPut,
		Path:        "/canonical-merchants/{id}",
		Summary:     "Rename a canonical merchant",
		Tags:        []string{"canonical-merchants"},
		Middlewares: huma.Middlewares{middleware.RequirePermission(perms, "labels:write")},
	}, h.RenameCanonicalMerchant)

	huma.Register(api, huma.Operation{
		OperationID:   "delete-canonical-merchant",
		Method:        http.MethodDelete,
		Path:          "/canonical-merchants/{id}",
		Summary:       "Delete a canonical merchant",
		Tags:          []string{"canonical-merchants"},
		DefaultStatus: http.StatusNoContent,
		Middlewares:   huma.Middlewares{middleware.RequirePermission(perms, "labels:write")},
	}, h.DeleteCanonicalMerchant)

	huma.Register(api, huma.Operation{
		OperationID: "list-canonical-merchant-aliases",
		Method:      http.MethodGet,
		Path:        "/canonical-merchants/{id}/aliases",
		Summary:     "List aliases for a canonical merchant",
		Tags:        []string{"canonical-merchants"},
		Middlewares: huma.Middlewares{middleware.RequirePermission(perms, "accounts:read")},
	}, h.ListCanonicalMerchantAliases)

	huma.Register(api, huma.Operation{
		OperationID: "add-canonical-merchant-alias",
		Method:      http.MethodPost,
		Path:        "/canonical-merchants/{id}/aliases",
		Summary:     "Add an alias to a canonical merchant",
		Tags:        []string{"canonical-merchants"},
		Middlewares: huma.Middlewares{middleware.RequirePermission(perms, "labels:write")},
	}, h.AddCanonicalMerchantAlias)

	huma.Register(api, huma.Operation{
		OperationID:   "delete-canonical-merchant-alias",
		Method:        http.MethodDelete,
		Path:          "/canonical-merchants/{id}/aliases/{normalized_name}",
		Summary:       "Remove an alias from a canonical merchant",
		Tags:          []string{"canonical-merchants"},
		DefaultStatus: http.StatusNoContent,
		Middlewares:   huma.Middlewares{middleware.RequirePermission(perms, "labels:write")},
	}, h.DeleteCanonicalMerchantAlias)

	huma.Register(api, huma.Operation{
		OperationID: "merge-canonical-merchant",
		Method:      http.MethodPost,
		Path:        "/canonical-merchants/{id}/merge",
		Summary:     "Merge another canonical merchant into this one",
		Tags:        []string{"canonical-merchants"},
		Middlewares: huma.Middlewares{middleware.RequirePermission(perms, "labels:write")},
	}, h.MergeCanonicalMerchant)

	huma.Register(api, huma.Operation{
		OperationID: "split-canonical-merchant",
		Method:      http.MethodPost,
		Path:        "/canonical-merchants/{id}/split",
		Summary:     "Split selected aliases into a new canonical merchant",
		Tags:        []string{"canonical-merchants"},
		Middlewares: huma.Middlewares{middleware.RequirePermission(perms, "labels:write")},
	}, h.SplitCanonicalMerchant)
}
