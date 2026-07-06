package authclient_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/veloci/api/internal/authclient"
)

func TestValidateToken_Success(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/tokens/validate" || r.Method != http.MethodPost {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{
			"jti":           "test-jti",
			"credential_id": "cred-1",
			"claims":        map[string]string{"sub": "user-1", "entity_id": "ent-1"},
		})
	}))
	defer srv.Close()

	c := authclient.New(srv.URL)
	result, err := c.ValidateToken(context.Background(), "some-token")
	if err != nil {
		t.Fatalf("ValidateToken: %v", err)
	}
	if result.JTI != "test-jti" {
		t.Errorf("jti: got %q want test-jti", result.JTI)
	}
	if result.CredentialID != "cred-1" {
		t.Errorf("credential_id: got %q want cred-1", result.CredentialID)
	}
	if len(result.Claims) == 0 {
		t.Error("expected non-empty claims")
	}
}

func TestValidateToken_Unauthorized(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, `{"code":"UNAUTHORIZED"}`, http.StatusUnauthorized)
	}))
	defer srv.Close()

	c := authclient.New(srv.URL)
	_, err := c.ValidateToken(context.Background(), "bad-token")
	if err == nil {
		t.Error("expected error for 401 response")
	}
}

func TestValidateCredential_Success(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/credentials/validate" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]string{
			"credential_id": "cred-1",
			"system_role":   "user",
		})
	}))
	defer srv.Close()

	c := authclient.New(srv.URL)
	result, err := c.ValidateCredential(context.Background(), "a@b.com", "pw")
	if err != nil {
		t.Fatalf("ValidateCredential: %v", err)
	}
	if result.CredentialID != "cred-1" {
		t.Errorf("credential_id: got %q", result.CredentialID)
	}
	if result.SystemRole != "user" {
		t.Errorf("system_role: got %q", result.SystemRole)
	}
}

func TestValidateCredential_Unauthorized(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, `{"code":"INVALID_CREDENTIALS"}`, http.StatusUnauthorized)
	}))
	defer srv.Close()

	c := authclient.New(srv.URL)
	_, err := c.ValidateCredential(context.Background(), "a@b.com", "wrong")
	if err == nil {
		t.Error("expected error for 401 response")
	}
}

func TestMintToken_Success(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/tokens/mint" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		json.NewEncoder(w).Encode(map[string]string{
			"token":      "eyJ.test.token",
			"jti":        "test-jti",
			"expires_at": "2099-01-01T00:00:00Z",
		})
	}))
	defer srv.Close()

	c := authclient.New(srv.URL)
	result, err := c.MintToken(context.Background(), "cred-1", map[string]any{"sub": "user-1"})
	if err != nil {
		t.Fatalf("MintToken: %v", err)
	}
	if result.Token != "eyJ.test.token" {
		t.Errorf("token: got %q", result.Token)
	}
	if result.JTI != "test-jti" {
		t.Errorf("jti: got %q", result.JTI)
	}
}

func TestCreateCredential_Success(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/credentials/create" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		json.NewEncoder(w).Encode(map[string]string{"credential_id": "new-cred-1"})
	}))
	defer srv.Close()

	c := authclient.New(srv.URL)
	id, err := c.CreateCredential(context.Background(), "new@b.com", "password123")
	if err != nil {
		t.Fatalf("CreateCredential: %v", err)
	}
	if id != "new-cred-1" {
		t.Errorf("credential_id: got %q", id)
	}
}

func TestCreateCredential_Conflict(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, `{"code":"CONFLICT"}`, http.StatusConflict)
	}))
	defer srv.Close()

	c := authclient.New(srv.URL)
	_, err := c.CreateCredential(context.Background(), "existing@b.com", "password123")
	if err == nil {
		t.Error("expected error for 409 response")
	}
}
