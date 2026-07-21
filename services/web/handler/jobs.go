package handler

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/labstack/echo/v4"
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

func (h *JobsHandler) ListJobs(c echo.Context) error {
	ctx := c.Request().Context()
	entityID := middleware.EntityID(ctx)

	cursor := c.QueryParam("cursor")
	limit, err := strconv.Atoi(c.QueryParam("limit"))
	if err != nil || limit <= 0 {
		limit = 50
	}

	items, err := h.s.ListJobs(ctx, entityID, limit+1, cursor)
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
		c := store.EncodeCursor(last.ID, last.QueuedAt)
		nextCursor = &c
	}

	views := make([]jobView, len(items))
	for i, item := range items {
		views[i] = toJobView(item)
	}
	return c.JSON(http.StatusOK, response.Page(views, nextCursor, limit, hasMore))
}

func (h *JobsHandler) TriggerReprocess(c echo.Context) error {
	ctx := c.Request().Context()
	entityID := middleware.EntityID(ctx)
	userID := middleware.UserID(ctx)

	meta, _ := json.Marshal(map[string]string{})
	job, err := h.s.CreateJob(ctx, entityID, "entries.reprocess", userID, meta)
	if err != nil {
		if strings.Contains(err.Error(), "unique") || strings.Contains(err.Error(), "processing_jobs_one_active") {
			return echo.NewHTTPError(http.StatusConflict, "a job of this type is already active")
		}
		return echo.NewHTTPError(http.StatusInternalServerError, "internal error")
	}

	h.pub.Publish(ctx, queue.Job{ //nolint:errcheck
		JobID:    job.ID,
		Type:     "entries.reprocess",
		EntityID: entityID,
		Metadata: meta,
	})

	return c.JSON(http.StatusOK, response.Single(toJobView(job)))
}

func (h *JobsHandler) TriggerAnalyze(c echo.Context) error {
	ctx := c.Request().Context()
	entityID := middleware.EntityID(ctx)
	userID := middleware.UserID(ctx)

	meta, _ := json.Marshal(map[string]string{})
	job, err := h.s.CreateJob(ctx, entityID, "account.analyze", userID, meta)
	if err != nil {
		if strings.Contains(err.Error(), "unique") || strings.Contains(err.Error(), "processing_jobs_one_active") {
			return echo.NewHTTPError(http.StatusConflict, "a job of this type is already active")
		}
		return echo.NewHTTPError(http.StatusInternalServerError, "internal error")
	}

	h.pub.Publish(ctx, queue.Job{ //nolint:errcheck
		JobID:    job.ID,
		Type:     "account.analyze",
		EntityID: entityID,
		Metadata: meta,
	})

	return c.JSON(http.StatusOK, response.Single(toJobView(job)))
}

func (h *JobsHandler) TriggerProject(c echo.Context) error {
	ctx := c.Request().Context()
	entityID := middleware.EntityID(ctx)
	userID := middleware.UserID(ctx)

	meta, _ := json.Marshal(map[string]string{})
	job, err := h.s.CreateJob(ctx, entityID, "balance.project", userID, meta)
	if err != nil {
		if strings.Contains(err.Error(), "unique") || strings.Contains(err.Error(), "processing_jobs_one_active") {
			return echo.NewHTTPError(http.StatusConflict, "a job of this type is already active")
		}
		return echo.NewHTTPError(http.StatusInternalServerError, "internal error")
	}

	h.pub.Publish(ctx, queue.Job{ //nolint:errcheck
		JobID:    job.ID,
		Type:     "balance.project",
		EntityID: entityID,
		Metadata: meta,
	})

	return c.JSON(http.StatusOK, response.Single(toJobView(job)))
}

// sseJobEvent is the payload sent over the SSE channel.
type sseJobEvent struct {
	JobID   string  `json:"job_id"`
	JobType string  `json:"job_type"`
	Status  string  `json:"status"`
	Error   *string `json:"error"`
}

// StreamJobs streams job state changes as Server-Sent Events.
// This handler is registered in main.go with its own auth middleware.
func (h *JobsHandler) StreamJobs(c echo.Context) error {
	ctx := c.Request().Context()
	entityID := middleware.EntityID(ctx)

	w := c.Response()
	flusher, ok := w.Writer.(http.Flusher)
	if !ok {
		return echo.NewHTTPError(http.StatusInternalServerError, "streaming not supported")
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")

	conn, err := h.pool.Acquire(ctx)
	if err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "internal error")
	}
	defer conn.Release()

	rawConn := conn.Conn()
	channel := "job:" + entityID
	if _, err := rawConn.Exec(ctx, "LISTEN "+pgxQuoteIdentifier(channel)); err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "internal error")
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
			return nil
		default:
		}

		notification, err := rawConn.WaitForNotification(ctx)
		if err != nil {
			if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
				return nil
			}
			return nil
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

// RegisterJobsRoutes registers job endpoints on the given Echo group.
func RegisterJobsRoutes(g *echo.Group, h *JobsHandler, perms middleware.PermissionCache) {
	read := g.Group("", middleware.RequirePermission(perms, "accounts:read"))
	write := g.Group("", middleware.RequirePermission(perms, "entries:write"))
	reports := g.Group("", middleware.RequirePermission(perms, "reports:read"))

	read.GET("/jobs", h.ListJobs)
	write.POST("/jobs/reprocess", h.TriggerReprocess)
	write.POST("/jobs/analyze", h.TriggerAnalyze)
	reports.POST("/jobs/project", h.TriggerProject)
}
