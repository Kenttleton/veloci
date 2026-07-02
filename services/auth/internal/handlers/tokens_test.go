package handlers_test

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/veloci/auth/internal/handlers"
)

type stubTokenDB struct {
	stored map[string]*handlers.TokenRow
}

func newStubTokenDB() *stubTokenDB {
	return &stubTokenDB{stored: map[string]*handlers.TokenRow{}}
}

func (s *stubTokenDB) StoreToken(_ context.Context, id, userID, jti string, claims json.RawMessage, exp time.Time) error {
	s.stored[jti] = &handlers.TokenRow{CredentialID: userID, Claims: claims, ExpiresAt: exp}
	return nil
}

func (s *stubTokenDB) FindToken(_ context.Context, jti string) (*handlers.TokenRow, error) {
	row, ok := s.stored[jti]
	if !ok {
		return nil, handlers.ErrNotFound
	}
	return row, nil
}

func (s *stubTokenDB) DeleteToken(_ context.Context, jti string) error {
	delete(s.stored, jti)
	return nil
}

func TestMintAndValidateToken(t *testing.T) {
	secret := []byte("test-secret-at-least-32-characters!!")
	db := newStubTokenDB()
	h := handlers.NewTokens(db, secret)

	mintBody, _ := json.Marshal(map[string]any{
		"credential_id": "cred-1",
		"claims": map[string]string{
			"sub": "user-1", "entity_id": "ent-1", "entity_role": "entity_admin",
		},
	})
	req := httptest.NewRequest(http.MethodPost, "/tokens/mint", bytes.NewReader(mintBody))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	h.Mint(w, req)

	if w.Code != http.StatusCreated {
		t.Fatalf("mint status: got %d; body: %s", w.Code, w.Body)
	}
	var mintResp map[string]string
	json.NewDecoder(w.Body).Decode(&mintResp)
	tok := mintResp["token"]
	if tok == "" {
		t.Fatal("expected token in mint response")
	}

	validateBody, _ := json.Marshal(map[string]string{"token": tok})
	req2 := httptest.NewRequest(http.MethodPost, "/tokens/validate", bytes.NewReader(validateBody))
	req2.Header.Set("Content-Type", "application/json")
	w2 := httptest.NewRecorder()
	h.Validate(w2, req2)

	if w2.Code != http.StatusOK {
		t.Fatalf("validate status: got %d; body: %s", w2.Code, w2.Body)
	}
	var validateResp map[string]any
	json.NewDecoder(w2.Body).Decode(&validateResp)
	if validateResp["credential_id"] != "cred-1" {
		t.Errorf("credential_id: got %v", validateResp["credential_id"])
	}
}
