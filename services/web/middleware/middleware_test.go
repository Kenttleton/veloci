package middleware_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/labstack/echo/v4"
	"github.com/veloci/veloci/authclient"
	"github.com/veloci/veloci/middleware"
)

func mustAuthClient(t *testing.T, url string) *authclient.Client {
	t.Helper()
	c, err := authclient.NewClient(url)
	if err != nil {
		t.Fatalf("authclient.NewClient(%q): %v", url, err)
	}
	return c
}

func mockAuthServer(claims map[string]any) *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/tokens/validate" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{
			"token_type":    "access",
			"jti":           "test-jti",
			"credential_id": "cred-1",
			"claims":        claims,
		})
	}))
}

func echoContext(req *http.Request, rec *httptest.ResponseRecorder) echo.Context {
	e := echo.New()
	return e.NewContext(req, rec)
}

func TestAuthMiddlewareInjectsClaims(t *testing.T) {
	srv := mockAuthServer(map[string]any{
		"sub": "user-1", "entity_id": "ent-1",
		"entity_role": "entity_admin", "system_role": "user",
	})
	defer srv.Close()

	client := mustAuthClient(t, srv.URL)
	var gotEntityID, gotEntityRole string

	next := func(c echo.Context) error {
		gotEntityID = middleware.EntityID(c.Request().Context())
		gotEntityRole = middleware.EntityRole(c.Request().Context())
		return c.NoContent(http.StatusOK)
	}

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("Authorization", "Bearer sometoken")
	rec := httptest.NewRecorder()
	c := echoContext(req, rec)

	err := middleware.Authenticate(client)(next)(c)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if rec.Code != http.StatusOK {
		t.Fatalf("status: got %d want 200", rec.Code)
	}
	if gotEntityID != "ent-1" {
		t.Errorf("entity_id: got %q", gotEntityID)
	}
	if gotEntityRole != "entity_admin" {
		t.Errorf("entity_role: got %q", gotEntityRole)
	}
}

func TestAuthMiddlewareRejectsMissingToken(t *testing.T) {
	client := mustAuthClient(t, "http://unused")
	next := func(c echo.Context) error { return c.NoContent(http.StatusOK) }

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	rec := httptest.NewRecorder()
	c := echoContext(req, rec)

	err := middleware.Authenticate(client)(next)(c)
	he, ok := err.(*echo.HTTPError)
	if !ok || he.Code != http.StatusUnauthorized {
		t.Errorf("expected 401 HTTPError, got %v", err)
	}
}

func TestAuthMiddlewareRejectsInvalidToken(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/problem+json")
		w.WriteHeader(http.StatusUnauthorized)
		json.NewEncoder(w).Encode(map[string]any{
			"status": 401,
			"title":  "Unauthorized",
			"detail": "invalid token",
		})
	}))
	defer srv.Close()

	client := mustAuthClient(t, srv.URL)
	next := func(c echo.Context) error { return c.NoContent(http.StatusOK) }

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("Authorization", "Bearer badtoken")
	rec := httptest.NewRecorder()
	c := echoContext(req, rec)

	err := middleware.Authenticate(client)(next)(c)
	he, ok := err.(*echo.HTTPError)
	if !ok || he.Code != http.StatusUnauthorized {
		t.Errorf("expected 401 HTTPError, got %v", err)
	}
}

func TestAuthMiddlewareRejectsInviteToken(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{
			"token_type": "invite",
			"claims":     map[string]string{"email": "invited@example.com"},
		})
	}))
	defer srv.Close()

	client := mustAuthClient(t, srv.URL)
	next := func(c echo.Context) error { return c.NoContent(http.StatusOK) }

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("Authorization", "Bearer invitetoken")
	rec := httptest.NewRecorder()
	c := echoContext(req, rec)

	err := middleware.Authenticate(client)(next)(c)
	he, ok := err.(*echo.HTTPError)
	if !ok || he.Code != http.StatusUnauthorized {
		t.Errorf("invite token: expected 401 HTTPError, got %v", err)
	}
}
