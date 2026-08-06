package handler

import (
	"encoding/json"
	"net/http"
	"time"

	"github.com/labstack/echo/v4"
	"github.com/veloci/veloci/middleware"
	"github.com/veloci/veloci/queue"
	"github.com/veloci/veloci/store"
)

// ProposalsHandler handles entry-proposal approve/reject endpoints.
type ProposalsHandler struct {
	s   *store.Store
	pub *queue.Publisher
}

// NewProposalsHandler creates a ProposalsHandler.
func NewProposalsHandler(s *store.Store, pub *queue.Publisher) *ProposalsHandler {
	return &ProposalsHandler{s: s, pub: pub}
}

func (h *ProposalsHandler) ApproveProposal(c echo.Context) error {
	ctx := c.Request().Context()
	entityID := middleware.EntityID(ctx)
	userID := middleware.UserID(ctx)
	id := c.Param("id")

	var proposalType string
	if err := h.s.GetProposalType(ctx, entityID, id, &proposalType); err != nil {
		return echo.NewHTTPError(http.StatusNotFound, "not found")
	}

	var err error
	switch proposalType {
	case "new":
		err = h.s.ApproveNewProposal(ctx, entityID, id, userID)
	case "ended":
		err = h.s.ApproveEndedProposal(ctx, entityID, id, userID)
	default:
		return echo.NewHTTPError(http.StatusBadRequest, "unknown proposal type")
	}
	if err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "internal error")
	}

	meta, _ := json.Marshal(map[string]string{})
	if job, jobErr := h.s.CreateJob(ctx, entityID, "account.analyze", userID, meta); jobErr == nil {
		h.pub.Publish(ctx, queue.Job{ //nolint:errcheck
			JobID:    job.ID,
			Type:     "account.analyze",
			EntityID: entityID,
			Metadata: meta,
		})
	}
	return c.NoContent(http.StatusNoContent)
}

func (h *ProposalsHandler) RejectProposal(c echo.Context) error {
	ctx := c.Request().Context()
	entityID := middleware.EntityID(ctx)
	userID := middleware.UserID(ctx)
	id := c.Param("id")

	var proposalType string
	if err := h.s.GetProposalType(ctx, entityID, id, &proposalType); err != nil {
		return echo.NewHTTPError(http.StatusNotFound, "not found")
	}

	var err error
	switch proposalType {
	case "new":
		err = h.s.RejectNewProposal(ctx, entityID, id, userID)
	case "ended":
		err = h.s.RejectEndedProposal(ctx, entityID, id, userID, time.Now().UTC())
	default:
		return echo.NewHTTPError(http.StatusBadRequest, "unknown proposal type")
	}
	if err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "internal error")
	}
	return c.NoContent(http.StatusNoContent)
}

// RegisterProposalsRoutes registers proposal endpoints on the given Echo group.
func RegisterProposalsRoutes(g *echo.Group, s *store.Store, pub *queue.Publisher, perms middleware.PermissionCache) {
	h := NewProposalsHandler(s, pub)

	write := g.Group("", middleware.RequirePermission(perms, "entries:write"))
	write.POST("/proposals/:id/approve", h.ApproveProposal)
	write.POST("/proposals/:id/reject", h.RejectProposal)
}
