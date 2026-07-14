package handler

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"

	"github.com/danielgtaylor/huma/v2"
	"github.com/go-faster/jx"
	"github.com/jackc/pgx/v5"
	"github.com/veloci/api/authclient"
	"github.com/veloci/api/middleware"
	"github.com/veloci/api/queue"
	"github.com/veloci/api/response"
	"github.com/veloci/api/store"
)

// UsersHandler handles user management endpoints.
type UsersHandler struct {
	s    *store.Store
	auth *authclient.Client
}

// NewUsersHandler creates a UsersHandler.
func NewUsersHandler(s *store.Store, auth *authclient.Client) *UsersHandler {
	return &UsersHandler{s: s, auth: auth}
}

// userView is the API representation of a user.
type userView struct {
	ID         string `json:"id"`
	Email      string `json:"email"`
	EntityRole string `json:"entity_role"`
	CreatedAt  string `json:"created_at"`
}

func toUserView(u store.User) userView {
	return userView{
		ID:         u.ID,
		Email:      u.Email,
		EntityRole: u.EntityRole,
		CreatedAt:  u.CreatedAt.Format("2006-01-02T15:04:05Z07:00"),
	}
}

type getMeOutput struct {
	Body response.Envelope[userView]
}

type updateMeInput struct {
	Body struct {
		Email string `json:"email" required:"true"`
	}
}

type updateMeOutput struct {
	Body response.Envelope[userView]
}

type listUsersOutput struct {
	Body response.Envelope[[]userView]
}

type changePasswordInput struct {
	PathID string `path:"id"`
	Body   struct {
		Password string `json:"password" required:"true"`
	}
}

type deleteUserInput struct {
	PathID string `path:"id"`
}

type inviteUserInput struct {
	Body struct {
		Email      string `json:"email"       required:"true"`
		EntityRole string `json:"entity_role" required:"true"`
	}
}

type inviteUserOutput struct {
	Body struct {
		Token     string `json:"token"`
		ExpiresAt string `json:"expires_at"`
	}
}

func (h *UsersHandler) GetMe(ctx context.Context, _ *struct{}) (*getMeOutput, error) {
	entityID := middleware.EntityID(ctx)
	userID := middleware.UserID(ctx)

	u, err := h.s.GetUserByID(ctx, entityID, userID)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, huma.Error404NotFound("not found")
	}
	if err != nil {
		return nil, huma.Error500InternalServerError("internal error")
	}
	out := &getMeOutput{}
	out.Body = response.Single(toUserView(u))
	return out, nil
}

func (h *UsersHandler) UpdateMe(ctx context.Context, _ *updateMeInput) (*updateMeOutput, error) {
	entityID := middleware.EntityID(ctx)
	userID := middleware.UserID(ctx)

	if err := h.s.UpdateUserProfile(ctx, userID); err != nil {
		return nil, huma.Error500InternalServerError("internal error")
	}
	u, err := h.s.GetUserByID(ctx, entityID, userID)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, huma.Error404NotFound("not found")
	}
	if err != nil {
		return nil, huma.Error500InternalServerError("internal error")
	}
	out := &updateMeOutput{}
	out.Body = response.Single(toUserView(u))
	return out, nil
}

func (h *UsersHandler) ListUsers(ctx context.Context, _ *struct{}) (*listUsersOutput, error) {
	entityID := middleware.EntityID(ctx)

	users, err := h.s.ListUsers(ctx, entityID)
	if err != nil {
		return nil, huma.Error500InternalServerError("internal error")
	}
	views := make([]userView, len(users))
	for i, u := range users {
		views[i] = toUserView(u)
	}
	out := &listUsersOutput{}
	out.Body = response.Single(views)
	return out, nil
}

func (h *UsersHandler) ChangePassword(ctx context.Context, input *changePasswordInput) (*struct{}, error) {
	entityID := middleware.EntityID(ctx)

	credID, err := h.s.GetUserCredentialID(ctx, input.PathID)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, huma.Error404NotFound("not found")
	}
	if err != nil {
		return nil, huma.Error500InternalServerError("internal error")
	}

	// Verify user belongs to entity.
	if _, err := h.s.GetUserByID(ctx, entityID, input.PathID); errors.Is(err, pgx.ErrNoRows) {
		return nil, huma.Error404NotFound("not found")
	} else if err != nil {
		return nil, huma.Error500InternalServerError("internal error")
	}

	if err := h.auth.UpdateCredentialPassword(ctx, &authclient.UpdateCredentialPasswordInputBody{
		Password: input.Body.Password,
	}, authclient.UpdateCredentialPasswordParams{ID: credID}); err != nil {
		return nil, huma.Error500InternalServerError("internal error")
	}
	return nil, nil
}

func (h *UsersHandler) DeleteUser(ctx context.Context, input *deleteUserInput) (*struct{}, error) {
	entityID := middleware.EntityID(ctx)

	credID, err := h.s.GetUserCredentialID(ctx, input.PathID)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, huma.Error404NotFound("not found")
	}
	if err != nil {
		return nil, huma.Error500InternalServerError("internal error")
	}

	// Verify user belongs to entity.
	if _, err := h.s.GetUserByID(ctx, entityID, input.PathID); errors.Is(err, pgx.ErrNoRows) {
		return nil, huma.Error404NotFound("not found")
	} else if err != nil {
		return nil, huma.Error500InternalServerError("internal error")
	}

	h.auth.RevokeUserTokens(ctx, authclient.RevokeUserTokensParams{CredentialID: credID}) //nolint:errcheck

	if err := h.s.DeleteUser(ctx, entityID, input.PathID); err != nil {
		return nil, huma.Error500InternalServerError("internal error")
	}
	return nil, nil
}

func (h *UsersHandler) InviteUser(ctx context.Context, input *inviteUserInput) (*inviteUserOutput, error) {
	userID := middleware.UserID(ctx)
	entityID := middleware.EntityID(ctx)

	claims := make(authclient.CreateInviteInputBodyClaims)
	for k, v := range map[string]string{
		"email":       input.Body.Email,
		"entity_id":   entityID,
		"entity_role": input.Body.EntityRole,
	} {
		b, _ := json.Marshal(v)
		claims[k] = jx.Raw(b)
	}

	result, err := h.auth.CreateInvite(ctx, &authclient.CreateInviteInputBody{
		Claims:    claims,
		CreatedBy: userID,
	})
	if err != nil {
		return nil, huma.Error500InternalServerError("internal error")
	}

	out := &inviteUserOutput{}
	out.Body.Token = result.Token
	out.Body.ExpiresAt = result.ExpiresAt
	return out, nil
}

// RegisterUsersRoutes registers user management endpoints on the given Huma API.
func RegisterUsersRoutes(api huma.API, s *store.Store, auth *authclient.Client, _ *queue.Publisher, perms middleware.PermissionCache) {
	h := NewUsersHandler(s, auth)

	huma.Register(api, huma.Operation{
		OperationID: "get-me",
		Method:      http.MethodGet,
		Path:        "/users/me",
		Summary:     "Get current user profile",
		Tags:        []string{"users"},
		Middlewares: huma.Middlewares{middleware.RequirePermission(perms, "accounts:read")},
	}, h.GetMe)

	huma.Register(api, huma.Operation{
		OperationID: "update-me",
		Method:      http.MethodPut,
		Path:        "/users/me",
		Summary:     "Update current user profile",
		Tags:        []string{"users"},
		Middlewares: huma.Middlewares{middleware.RequirePermission(perms, "accounts:read")},
	}, h.UpdateMe)

	huma.Register(api, huma.Operation{
		OperationID: "list-users",
		Method:      http.MethodGet,
		Path:        "/users",
		Summary:     "List all entity users",
		Tags:        []string{"users"},
		Middlewares: huma.Middlewares{middleware.RequirePermission(perms, "users:manage")},
	}, h.ListUsers)

	huma.Register(api, huma.Operation{
		OperationID:   "change-user-password",
		Method:        http.MethodPut,
		Path:          "/users/{id}/password",
		Summary:       "Change a user password",
		Tags:          []string{"users"},
		DefaultStatus: http.StatusNoContent,
		Middlewares:   huma.Middlewares{middleware.RequirePermission(perms, "users:manage")},
	}, h.ChangePassword)

	huma.Register(api, huma.Operation{
		OperationID:   "delete-user",
		Method:        http.MethodDelete,
		Path:          "/users/{id}",
		Summary:       "Remove a user from the entity",
		Tags:          []string{"users"},
		DefaultStatus: http.StatusNoContent,
		Middlewares:   huma.Middlewares{middleware.RequirePermission(perms, "users:manage")},
	}, h.DeleteUser)

	huma.Register(api, huma.Operation{
		OperationID: "invite-user",
		Method:      http.MethodPost,
		Path:        "/users/invite",
		Summary:     "Send a user invite",
		Tags:        []string{"users"},
		Middlewares: huma.Middlewares{middleware.RequirePermission(perms, "users:manage")},
	}, h.InviteUser)
}
