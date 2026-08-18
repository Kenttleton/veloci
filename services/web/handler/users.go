package handler

import (
	"encoding/json"
	"errors"
	"net/http"

	"github.com/go-faster/jx"
	"github.com/jackc/pgx/v5"
	"github.com/labstack/echo/v4"
	"github.com/veloci/veloci/authclient"
	"github.com/veloci/veloci/middleware"
	"github.com/veloci/veloci/response"
	"github.com/veloci/veloci/store"
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
	ID            string `json:"id"`
	Email         string `json:"email"`
	FirstName     string `json:"first_name"`
	LastName      string `json:"last_name"`
	PreferredName string `json:"preferred_name"`
	EntityRole    string `json:"entity_role"`
	CreatedAt     string `json:"created_at"`
}

func toUserView(u store.User) userView {
	return userView{
		ID:            u.ID,
		Email:         u.Email,
		FirstName:     u.FirstName,
		LastName:      u.LastName,
		PreferredName: u.PreferredName,
		EntityRole:    u.EntityRole,
		CreatedAt:     u.CreatedAt.Format("2006-01-02T15:04:05Z07:00"),
	}
}

func (h *UsersHandler) GetMe(c echo.Context) error {
	ctx := c.Request().Context()
	entityID := middleware.EntityID(ctx)
	userID := middleware.UserID(ctx)

	u, err := h.s.GetUserByID(ctx, entityID, userID)
	if errors.Is(err, pgx.ErrNoRows) {
		return echo.NewHTTPError(http.StatusNotFound, "not found")
	}
	if err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "internal error")
	}
	return c.JSON(http.StatusOK, response.Single(toUserView(u)))
}

func (h *UsersHandler) UpdateMe(c echo.Context) error {
	ctx := c.Request().Context()
	entityID := middleware.EntityID(ctx)
	userID := middleware.UserID(ctx)

	var body struct {
		FirstName     string `json:"first_name"`
		LastName      string `json:"last_name"`
		PreferredName string `json:"preferred_name"`
	}
	if err := c.Bind(&body); err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, "invalid body")
	}

	if err := h.s.UpdateUserProfile(ctx, userID, body.FirstName, body.LastName, body.PreferredName); err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "internal error")
	}
	u, err := h.s.GetUserByID(ctx, entityID, userID)
	if errors.Is(err, pgx.ErrNoRows) {
		return echo.NewHTTPError(http.StatusNotFound, "not found")
	}
	if err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "internal error")
	}
	return c.JSON(http.StatusOK, response.Single(toUserView(u)))
}

func (h *UsersHandler) ChangeOwnPassword(c echo.Context) error {
	ctx := c.Request().Context()
	email := middleware.Email(ctx)
	userID := middleware.UserID(ctx)

	var body struct {
		CurrentPassword string `json:"current_password"`
		NewPassword     string `json:"new_password"`
	}
	if err := c.Bind(&body); err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, "invalid body")
	}
	if body.CurrentPassword == "" || body.NewPassword == "" {
		return echo.NewHTTPError(http.StatusBadRequest, "current_password and new_password are required")
	}

	// Verify current password.
	if _, err := h.auth.ValidateCredential(ctx, &authclient.ValidateCredentialInputBody{
		Email:    email,
		Password: body.CurrentPassword,
	}); err != nil {
		return echo.NewHTTPError(http.StatusUnauthorized, "current password is incorrect")
	}

	credID, err := h.s.GetUserCredentialID(ctx, userID)
	if errors.Is(err, pgx.ErrNoRows) {
		return echo.NewHTTPError(http.StatusNotFound, "not found")
	}
	if err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "internal error")
	}

	if err := h.auth.UpdateCredentialPassword(ctx, &authclient.UpdateCredentialPasswordInputBody{
		Password: body.NewPassword,
	}, authclient.UpdateCredentialPasswordParams{ID: credID}); err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "internal error")
	}
	return c.NoContent(http.StatusNoContent)
}

func (h *UsersHandler) ChangeOwnEmail(c echo.Context) error {
	ctx := c.Request().Context()
	email := middleware.Email(ctx)
	userID := middleware.UserID(ctx)

	var body struct {
		Email           string `json:"email"`
		CurrentPassword string `json:"current_password"`
	}
	if err := c.Bind(&body); err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, "invalid body")
	}
	if body.Email == "" || body.CurrentPassword == "" {
		return echo.NewHTTPError(http.StatusBadRequest, "email and current_password are required")
	}

	// Verify current password before changing the email.
	if _, err := h.auth.ValidateCredential(ctx, &authclient.ValidateCredentialInputBody{
		Email:    email,
		Password: body.CurrentPassword,
	}); err != nil {
		return echo.NewHTTPError(http.StatusUnauthorized, "current password is incorrect")
	}

	credID, err := h.s.GetUserCredentialID(ctx, userID)
	if errors.Is(err, pgx.ErrNoRows) {
		return echo.NewHTTPError(http.StatusNotFound, "not found")
	}
	if err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "internal error")
	}

	// Update auth service first.
	if err := h.auth.UpdateCredentialEmail(ctx, &authclient.UpdateCredentialEmailInputBody{
		Email: body.Email,
	}, authclient.UpdateCredentialEmailParams{ID: credID}); err != nil {
		return echo.NewHTTPError(http.StatusConflict, "email already registered")
	}

	// Update app DB; roll back auth service on failure.
	if err := h.s.UpdateUserEmail(ctx, userID, body.Email); err != nil {
		// Best-effort rollback to keep auth and app in sync.
		_ = h.auth.UpdateCredentialEmail(ctx, &authclient.UpdateCredentialEmailInputBody{
			Email: email,
		}, authclient.UpdateCredentialEmailParams{ID: credID})
		return echo.NewHTTPError(http.StatusInternalServerError, "internal error")
	}
	return c.NoContent(http.StatusNoContent)
}

func (h *UsersHandler) ListUsers(c echo.Context) error {
	ctx := c.Request().Context()
	entityID := middleware.EntityID(ctx)

	users, err := h.s.ListUsers(ctx, entityID)
	if err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "internal error")
	}
	views := make([]userView, len(users))
	for i, u := range users {
		views[i] = toUserView(u)
	}
	return c.JSON(http.StatusOK, response.Single(views))
}

func (h *UsersHandler) ChangePassword(c echo.Context) error {
	ctx := c.Request().Context()
	entityID := middleware.EntityID(ctx)
	id := c.Param("id")

	var body struct {
		Password string `json:"password"`
	}
	if err := c.Bind(&body); err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, "invalid body")
	}

	credID, err := h.s.GetUserCredentialID(ctx, id)
	if errors.Is(err, pgx.ErrNoRows) {
		return echo.NewHTTPError(http.StatusNotFound, "not found")
	}
	if err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "internal error")
	}

	// Verify user belongs to entity.
	if _, err := h.s.GetUserByID(ctx, entityID, id); errors.Is(err, pgx.ErrNoRows) {
		return echo.NewHTTPError(http.StatusNotFound, "not found")
	} else if err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "internal error")
	}

	if err := h.auth.UpdateCredentialPassword(ctx, &authclient.UpdateCredentialPasswordInputBody{
		Password: body.Password,
	}, authclient.UpdateCredentialPasswordParams{ID: credID}); err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "internal error")
	}
	return c.NoContent(http.StatusNoContent)
}

func (h *UsersHandler) DeleteUser(c echo.Context) error {
	ctx := c.Request().Context()
	entityID := middleware.EntityID(ctx)
	id := c.Param("id")

	credID, err := h.s.GetUserCredentialID(ctx, id)
	if errors.Is(err, pgx.ErrNoRows) {
		return echo.NewHTTPError(http.StatusNotFound, "not found")
	}
	if err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "internal error")
	}

	// Verify user belongs to entity.
	if _, err := h.s.GetUserByID(ctx, entityID, id); errors.Is(err, pgx.ErrNoRows) {
		return echo.NewHTTPError(http.StatusNotFound, "not found")
	} else if err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "internal error")
	}

	h.auth.RevokeUserTokens(ctx, authclient.RevokeUserTokensParams{CredentialID: credID}) //nolint:errcheck

	if err := h.s.DeleteUser(ctx, entityID, id); err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "internal error")
	}
	return c.NoContent(http.StatusNoContent)
}

func (h *UsersHandler) InviteUser(c echo.Context) error {
	ctx := c.Request().Context()
	userID := middleware.UserID(ctx)
	entityID := middleware.EntityID(ctx)

	var body struct {
		Email      string `json:"email"`
		EntityRole string `json:"entity_role"`
	}
	if err := c.Bind(&body); err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, "invalid body")
	}

	claims := make(authclient.CreateInviteInputBodyClaims)
	for k, v := range map[string]string{
		"email":       body.Email,
		"entity_id":   entityID,
		"entity_role": body.EntityRole,
	} {
		b, _ := json.Marshal(v)
		claims[k] = jx.Raw(b)
	}

	result, err := h.auth.CreateInvite(ctx, &authclient.CreateInviteInputBody{
		Claims:    claims,
		CreatedBy: userID,
	})
	if err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "internal error")
	}

	return c.JSON(http.StatusOK, struct {
		Token     string `json:"token"`
		ExpiresAt string `json:"expires_at"`
	}{
		Token:     result.Token,
		ExpiresAt: result.ExpiresAt,
	})
}

// RegisterUsersRoutes registers user management endpoints on the given Echo group.
func RegisterUsersRoutes(g *echo.Group, s *store.Store, auth *authclient.Client, perms middleware.PermissionCache) {
	h := NewUsersHandler(s, auth)

	read := g.Group("", middleware.RequirePermission(perms, "accounts:read"))
	manage := g.Group("", middleware.RequirePermission(perms, "users:manage"))

	read.GET("/users/me", h.GetMe)
	read.PUT("/users/me", h.UpdateMe)
	g.PUT("/users/me/password", h.ChangeOwnPassword)
	g.PUT("/users/me/email", h.ChangeOwnEmail)
	manage.GET("/users", h.ListUsers)
	manage.PUT("/users/:id/password", h.ChangePassword)
	manage.DELETE("/users/:id", h.DeleteUser)
	manage.POST("/users/invite", h.InviteUser)
}
