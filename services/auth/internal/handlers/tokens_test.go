package handlers_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/veloci/auth/internal/handlers"
)

// stubTokenDB implements tokenStore and inviteStore for tests.
type stubTokenDB struct {
	stored       map[string]*handlers.TokenRow
	invites      map[string]*handlers.InviteTokenRow
	rotateErr    error
}

func newStubTokenDB() *stubTokenDB {
	return &stubTokenDB{
		stored:  map[string]*handlers.TokenRow{},
		invites: map[string]*handlers.InviteTokenRow{},
	}
}

func (s *stubTokenDB) StoreToken(_ context.Context, id, userID, jti string, claims json.RawMessage, exp time.Time, tokenType string, parentID *string) error {
	s.stored[jti] = &handlers.TokenRow{CredentialID: userID, Claims: claims, ExpiresAt: exp, TokenType: tokenType}
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

func (s *stubTokenDB) DeleteUserTokens(_ context.Context, credentialID string) error {
	for jti, row := range s.stored {
		if row.CredentialID == credentialID {
			delete(s.stored, jti)
		}
	}
	return nil
}

func (s *stubTokenDB) RotateRefreshToken(_ context.Context, oldJTI string, _ time.Duration) error {
	if s.rotateErr != nil {
		return s.rotateErr
	}
	// Mark the old refresh token as rotated.
	if row, ok := s.stored[oldJTI]; ok {
		now := time.Now()
		row.RotatedAt = &now
	}
	return nil
}

func (s *stubTokenDB) FindInviteToken(_ context.Context, tokenHash string) (*handlers.InviteTokenRow, error) {
	row, ok := s.invites[tokenHash]
	if !ok {
		return nil, handlers.ErrNotFound
	}
	return row, nil
}

func (s *stubTokenDB) StoreInviteToken(_ context.Context, tokenHash, createdBy string, claims []byte, expiresAt time.Time) error {
	s.invites[tokenHash] = &handlers.InviteTokenRow{
		Claims:    json.RawMessage(claims),
		ExpiresAt: expiresAt,
	}
	return nil
}

func (s *stubTokenDB) ConsumeInviteToken(_ context.Context, tokenHash string) (found bool, alreadyConsumed bool, expired bool, err error) {
	row, ok := s.invites[tokenHash]
	if !ok {
		return false, false, false, nil
	}
	if row.AcceptedAt != nil {
		return true, true, false, nil
	}
	if time.Now().After(row.ExpiresAt) {
		return true, false, true, nil
	}
	now := time.Now()
	row.AcceptedAt = &now
	return true, false, false, nil
}

func sha256hex(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}

var testSecret = []byte("test-secret-at-least-32-characters!!")

func newTestHandlers() (*handlers.Tokens, *handlers.Invite, *stubTokenDB) {
	db := newStubTokenDB()
	cfg := handlers.TokenConfig{
		AccessTTL:  15 * time.Minute,
		RefreshTTL: 24 * time.Hour,
	}
	toks := handlers.NewTokens(db, testSecret, cfg)
	inv := handlers.NewInvite(db, handlers.InviteConfig{TTL: 72 * time.Hour})
	return toks, inv, db
}

// mintToken is a test helper that calls /tokens/mint and returns the parsed response.
func mintToken(t *testing.T, h *handlers.Tokens, credentialID string) map[string]any {
	t.Helper()
	body, _ := json.Marshal(map[string]any{
		"credential_id": credentialID,
		"claims":        map[string]string{"sub": "user-1", "entity_id": "ent-1"},
	})
	req := httptest.NewRequest(http.MethodPost, "/tokens/mint", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	h.Mint(w, req)
	if w.Code != http.StatusCreated {
		t.Fatalf("mint: got %d; body: %s", w.Code, w.Body)
	}
	var resp map[string]any
	json.NewDecoder(w.Body).Decode(&resp)
	return resp
}

func TestMintAndValidateToken(t *testing.T) {
	h, _, _ := newTestHandlers()
	mintResp := mintToken(t, h, "cred-1")

	accessTok, _ := mintResp["access_token"].(string)
	if accessTok == "" {
		t.Fatal("expected access_token in mint response")
	}

	validateBody, _ := json.Marshal(map[string]string{"token": accessTok})
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
	if validateResp["token_type"] != "access" {
		t.Errorf("token_type: got %v want access", validateResp["token_type"])
	}
}

func TestMint_NilClaimsRejected(t *testing.T) {
	h, _, _ := newTestHandlers()
	body := []byte(`{"credential_id":"cred-1","claims":null}`)
	req := httptest.NewRequest(http.MethodPost, "/tokens/mint", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	h.Mint(w, req)
	if w.Code != http.StatusBadRequest {
		t.Errorf("expected 400 for null claims, got %d", w.Code)
	}
}

func TestValidate_UserNotFound(t *testing.T) {
	// Valid JWT signature but no matching row in the tokens table.
	h, _, db := newTestHandlers()
	mintResp := mintToken(t, h, "cred-1")
	accessTok, _ := mintResp["access_token"].(string)

	// Remove all stored tokens to simulate not found.
	for k := range db.stored {
		delete(db.stored, k)
	}

	validateBody, _ := json.Marshal(map[string]string{"token": accessTok})
	req := httptest.NewRequest(http.MethodPost, "/tokens/validate", bytes.NewReader(validateBody))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	h.Validate(w, req)

	if w.Code != http.StatusUnauthorized {
		t.Errorf("expected 401 for missing DB row, got %d", w.Code)
	}
}

// chiRequest builds an *http.Request with a chi route context populated with the given params.
func chiRequest(method, path string, body *bytes.Reader, params map[string]string) *http.Request {
	var req *http.Request
	if body != nil {
		req = httptest.NewRequest(method, path, body)
	} else {
		req = httptest.NewRequest(method, path, nil)
	}
	rctx := chi.NewRouteContext()
	for k, v := range params {
		rctx.URLParams.Add(k, v)
	}
	return req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))
}

func TestRevokeAndValidate(t *testing.T) {
	h, _, _ := newTestHandlers()
	mintResp := mintToken(t, h, "cred-1")
	accessTok, _ := mintResp["access_token"].(string)
	jti, _ := mintResp["jti"].(string)

	// Revoke the access token via chi-routed request so URLParam("jti") resolves.
	req := chiRequest(http.MethodDelete, "/tokens/"+jti, nil, map[string]string{"jti": jti})
	w := httptest.NewRecorder()
	h.Revoke(w, req)
	if w.Code != http.StatusNoContent {
		t.Fatalf("revoke: got %d", w.Code)
	}

	// Validate the revoked token — must be 401.
	validateBody, _ := json.Marshal(map[string]string{"token": accessTok})
	req2 := httptest.NewRequest(http.MethodPost, "/tokens/validate", bytes.NewReader(validateBody))
	req2.Header.Set("Content-Type", "application/json")
	w2 := httptest.NewRecorder()
	h.Validate(w2, req2)
	if w2.Code != http.StatusUnauthorized {
		t.Errorf("expected 401 after revoke, got %d", w2.Code)
	}
}

func TestValidate_ExpiredDBRecord(t *testing.T) {
	h, _, db := newTestHandlers()
	mintResp := mintToken(t, h, "cred-1")
	jti, _ := mintResp["jti"].(string)
	accessTok, _ := mintResp["access_token"].(string)

	// Backdate the expiry in the stub.
	if row, ok := db.stored[jti]; ok {
		row.ExpiresAt = time.Now().Add(-time.Minute)
	}

	validateBody, _ := json.Marshal(map[string]string{"token": accessTok})
	req := httptest.NewRequest(http.MethodPost, "/tokens/validate", bytes.NewReader(validateBody))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	h.Validate(w, req)

	if w.Code != http.StatusUnauthorized {
		t.Errorf("expected 401 for expired DB record, got %d", w.Code)
	}
}

func TestValidate_InviteToken(t *testing.T) {
	h, inv, db := newTestHandlers()

	// Create an invite token via the invite handler.
	body, _ := json.Marshal(map[string]any{
		"created_by": "cred-admin",
		"claims":     map[string]string{"email": "invited@example.com", "entity_id": "ent-1"},
	})
	req := httptest.NewRequest(http.MethodPost, "/invite", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	inv.CreateInvite(w, req)
	if w.Code != http.StatusCreated {
		t.Fatalf("create invite: got %d; body: %s", w.Code, w.Body)
	}
	var invResp map[string]string
	json.NewDecoder(w.Body).Decode(&invResp)
	rawToken := invResp["token"]
	if rawToken == "" {
		t.Fatal("expected token in invite response")
	}

	// The stub stores by hash; wire it for FindInviteToken via the token adapter.
	// stubTokenDB.FindInviteToken is already wired — the invite handler stored via StoreInviteToken.
	_ = db // db is already populated via inv's StoreInviteToken call

	// Validate the raw invite token through the tokens validate endpoint.
	validateBody, _ := json.Marshal(map[string]string{"token": rawToken})
	req2 := httptest.NewRequest(http.MethodPost, "/tokens/validate", bytes.NewReader(validateBody))
	req2.Header.Set("Content-Type", "application/json")
	w2 := httptest.NewRecorder()
	h.Validate(w2, req2)

	if w2.Code != http.StatusOK {
		t.Fatalf("validate invite token: got %d; body: %s", w2.Code, w2.Body)
	}
	var resp map[string]any
	json.NewDecoder(w2.Body).Decode(&resp)
	if resp["token_type"] != "invite" {
		t.Errorf("token_type: got %v want invite", resp["token_type"])
	}
}

func TestConsumeInvite_AlreadyConsumed(t *testing.T) {
	_, inv, _ := newTestHandlers()

	// Create invite.
	body, _ := json.Marshal(map[string]any{
		"created_by": "cred-admin",
		"claims":     map[string]string{"email": "invited@example.com"},
	})
	req := httptest.NewRequest(http.MethodPost, "/invite", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	inv.CreateInvite(w, req)
	var invResp map[string]string
	json.NewDecoder(w.Body).Decode(&invResp)
	rawToken := invResp["token"]

	consume := func() int {
		consumeBody, _ := json.Marshal(map[string]string{"token": rawToken})
		req := httptest.NewRequest(http.MethodPost, "/invite/consume", bytes.NewReader(consumeBody))
		req.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		inv.ConsumeInvite(w, req)
		return w.Code
	}

	if code := consume(); code != http.StatusNoContent {
		t.Fatalf("first consume: expected 204, got %d", code)
	}
	if code := consume(); code != http.StatusConflict {
		t.Errorf("second consume: expected 409, got %d", code)
	}
}

func TestConsumeInvite_Expired(t *testing.T) {
	_, inv, db := newTestHandlers()

	// Create invite.
	body, _ := json.Marshal(map[string]any{
		"created_by": "cred-admin",
		"claims":     map[string]string{"email": "invited@example.com"},
	})
	req := httptest.NewRequest(http.MethodPost, "/invite", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	inv.CreateInvite(w, req)
	var invResp map[string]string
	json.NewDecoder(w.Body).Decode(&invResp)
	rawToken := invResp["token"]

	// Backdate the expiry.
	hash := sha256hex(rawToken)
	if row, ok := db.invites[hash]; ok {
		row.ExpiresAt = time.Now().Add(-time.Minute)
	}

	consumeBody, _ := json.Marshal(map[string]string{"token": rawToken})
	req2 := httptest.NewRequest(http.MethodPost, "/invite/consume", bytes.NewReader(consumeBody))
	req2.Header.Set("Content-Type", "application/json")
	w2 := httptest.NewRecorder()
	inv.ConsumeInvite(w2, req2)
	if w2.Code != http.StatusGone {
		t.Errorf("expected 410 for expired invite, got %d", w2.Code)
	}
}
