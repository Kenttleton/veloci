package handler

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/danielgtaylor/huma/v2"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/veloci/veloci/middleware"
	"github.com/veloci/veloci/queue"
	"github.com/veloci/veloci/response"
	"github.com/veloci/veloci/store"
)

// JobsHandler handles processing job endpoints.
type JobsHandler struct {
	s    *store.Store
	pub  *queue.Publisher
	pool *pgxpool.Pool
}

// NewJobsHandler creates a JobsHandler.
func NewJobsHandler(s *store.Store, pub *queue.Publisher, pool *pgxpool.Pool) *JobsHandler {
	return &JobsHandler{s: s, pub: pub, pool: pool}
}

// jobView is the API representation of a processing_job.
type jobView struct {
	ID          string          `json:"id"`
	JobType     string          `json:"job_type"`
	TriggeredBy string          `json:"triggered_by"`
	Status      string          `json:"status"`
	QueuedAt    string          `json:"queued_at"`
	StartedAt   *string         `json:"started_at"`
	CompletedAt *string         `json:"completed_at"`
	Error       *string         `json:"error"`
	Metadata    json.RawMessage `json:"metadata"`
}

func toJobView(j store.ProcessingJob) jobView {
	v := jobView{
		ID:          j.ID,
		JobType:     j.JobType,
		TriggeredBy: j.TriggeredBy,
		Status:      j.Status,
		QueuedAt:    j.QueuedAt.Format("2006-01-02T15:04:05Z07:00"),
		Error:       j.Error,
		Metadata:    j.Metadata,
	}
	if j.StartedAt != nil {
		s := j.StartedAt.Format("2006-01-02T15:04:05Z07:00")
		v.StartedAt = &s
	}
	if j.CompletedAt != nil {
		s := j.CompletedAt.Format("2006-01-02T15:04:05Z07:00")
		v.CompletedAt = &s
	}
	return v
}

type listJobsInput struct {
	Cursor string `query:"cursor"`
	Limit  int    `query:"limit" default:"50" minimum:"1" maximum:"200"`
}

type listJobsOutput struct {
	Body response.Envelope[[]jobView]
}

type triggerJobOutput struct {
	Body response.Envelope[jobView]
}

func (h *JobsHandler) ListJobs(ctx context.Context, input *listJobsInput) (*listJobsOutput, error) {
	entityID := middleware.EntityID(ctx)
	limit := input.Limit
	if limit == 0 {
		limit = 50
	}

	items, err := h.s.ListJobs(ctx, entityID, limit+1, input.Cursor)
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
		c := store.EncodeCursor(last.ID, last.QueuedAt)
		nextCursor = &c
	}

	views := make([]jobView, len(items))
	for i, item := range items {
		views[i] = toJobView(item)
	}
	out := &listJobsOutput{}
	out.Body = response.Page(views, nextCursor, limit, hasMore)
	return out, nil
}

func (h *JobsHandler) TriggerReprocess(ctx context.Context, _ *struct{}) (*triggerJobOutput, error) {
	entityID := middleware.EntityID(ctx)
	userID := middleware.UserID(ctx)

	meta, _ := json.Marshal(map[string]string{})
	job, err := h.s.CreateJob(ctx, entityID, "entries.reprocess", userID, meta)
	if err != nil {
		if strings.Contains(err.Error(), "unique") || strings.Contains(err.Error(), "processing_jobs_one_active") {
			return nil, huma.Error409Conflict("a job of this type is already active")
		}
		return nil, huma.Error500InternalServerError("internal error")
	}

	h.pub.Publish(ctx, queue.Job{ //nolint:errcheck
		JobID:    job.ID,
		Type:     "entries.reprocess",
		EntityID: entityID,
		Metadata: meta,
	})

	out := &triggerJobOutput{}
	out.Body = response.Single(toJobView(job))
	return out, nil
}

func (h *JobsHandler) TriggerAnalyze(ctx context.Context, _ *struct{}) (*triggerJobOutput, error) {
	entityID := middleware.EntityID(ctx)
	userID := middleware.UserID(ctx)

	meta, _ := json.Marshal(map[string]string{})
	job, err := h.s.CreateJob(ctx, entityID, "account.analyze", userID, meta)
	if err != nil {
		if strings.Contains(err.Error(), "unique") || strings.Contains(err.Error(), "processing_jobs_one_active") {
			return nil, huma.Error409Conflict("a job of this type is already active")
		}
		return nil, huma.Error500InternalServerError("internal error")
	}

	h.pub.Publish(ctx, queue.Job{ //nolint:errcheck
		JobID:    job.ID,
		Type:     "account.analyze",
		EntityID: entityID,
		Metadata: meta,
	})

	out := &triggerJobOutput{}
	out.Body = response.Single(toJobView(job))
	return out, nil
}

func (h *JobsHandler) TriggerProject(ctx context.Context, _ *struct{}) (*triggerJobOutput, error) {
	entityID := middleware.EntityID(ctx)
	userID := middleware.UserID(ctx)

	meta, _ := json.Marshal(map[string]string{})
	job, err := h.s.CreateJob(ctx, entityID, "balance.project", userID, meta)
	if err != nil {
		if strings.Contains(err.Error(), "unique") || strings.Contains(err.Error(), "processing_jobs_one_active") {
			return nil, huma.Error409Conflict("a job of this type is already active")
		}
		return nil, huma.Error500InternalServerError("internal error")
	}

	h.pub.Publish(ctx, queue.Job{ //nolint:errcheck
		JobID:    job.ID,
		Type:     "balance.project",
		EntityID: entityID,
		Metadata: meta,
	})

	out := &triggerJobOutput{}
	out.Body = response.Single(toJobView(job))
	return out, nil
}

// sseJobEvent is the payload sent over the SSE channel.
type sseJobEvent struct {
	JobID    string  `json:"job_id"`
	JobType  string  `json:"job_type"`
	Status   string  `json:"status"`
	Error    *string `json:"error"`
}

// StreamJobs streams job state changes as Server-Sent Events.
// This handler must be registered directly on the chi router inside the authenticated group.
func (h *JobsHandler) StreamJobs(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	entityID := middleware.EntityID(ctx)

	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming not supported", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")

	conn, err := h.pool.Acquire(ctx)
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	defer conn.Release()

	rawConn := conn.Conn()
	channel := "job:" + entityID
	if _, err := rawConn.Exec(ctx, "LISTEN "+pgxQuoteIdentifier(channel)); err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	activeJobs, err := h.s.ListActiveJobs(ctx, entityID)
	if err == nil {
		for _, j := range activeJobs {
			event := sseJobEvent{JobID: j.ID, JobType: j.JobType, Status: j.Status, Error: j.Error}
			b, _ := json.Marshal(event)
			fmt.Fprintf(w, "data: %s\n\n", b)
		}
		flusher.Flush()
	}

	for {
		select {
		case <-ctx.Done():
			return
		default:
		}

		notification, err := rawConn.WaitForNotification(ctx)
		if err != nil {
			if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
				return
			}
			return
		}

		var event sseJobEvent
		if err := json.Unmarshal([]byte(notification.Payload), &event); err != nil {
			continue
		}

		if event.JobType == "import.process" && event.Status == "complete" {
			go func(jobID string) { //nolint:errcheck
				h.s.RecalculateBalanceForJob(context.Background(), entityID, jobID)
			}(event.JobID)
		}

		b, _ := json.Marshal(event)
		fmt.Fprintf(w, "data: %s\n\n", b)
		flusher.Flush()
	}
}

// pgxQuoteIdentifier safely quotes a PostgreSQL identifier.
func pgxQuoteIdentifier(name string) string {
	return `"` + strings.ReplaceAll(name, `"`, `""`) + `"`
}

// RegisterJobsRoutes registers job endpoints on the given Huma API.
func RegisterJobsRoutes(api huma.API, h *JobsHandler, perms middleware.PermissionCache) {
	huma.Register(api, huma.Operation{
		OperationID: "list-jobs",
		Method:      http.MethodGet,
		Path:        "/jobs",
		Summary:     "List processing jobs",
		Tags:        []string{"jobs"},
		Middlewares: huma.Middlewares{middleware.RequirePermission(perms, "accounts:read")},
	}, h.ListJobs)

	huma.Register(api, huma.Operation{
		OperationID: "trigger-reprocess",
		Method:      http.MethodPost,
		Path:        "/jobs/reprocess",
		Summary:     "Trigger an entries reprocess job",
		Tags:        []string{"jobs"},
		Middlewares: huma.Middlewares{middleware.RequirePermission(perms, "entries:write")},
	}, h.TriggerReprocess)

	huma.Register(api, huma.Operation{
		OperationID: "trigger-analyze",
		Method:      http.MethodPost,
		Path:        "/jobs/analyze",
		Summary:     "Trigger an account analyze job",
		Tags:        []string{"jobs"},
		Middlewares: huma.Middlewares{middleware.RequirePermission(perms, "entries:write")},
	}, h.TriggerAnalyze)

	huma.Register(api, huma.Operation{
		OperationID: "trigger-project",
		Method:      http.MethodPost,
		Path:        "/jobs/project",
		Summary:     "Trigger a balance projection job",
		Tags:        []string{"jobs"},
		Middlewares: huma.Middlewares{middleware.RequirePermission(perms, "reports:read")},
	}, h.TriggerProject)

}
