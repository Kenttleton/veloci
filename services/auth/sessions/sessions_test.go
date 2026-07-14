package sessions_test

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

	"github.com/danielgtaylor/huma/v2"
	"github.com/danielgtaylor/huma/v2/adapters/humachi"
	"github.com/go-chi/chi/v5"
	"github.com/veloci/auth/invites"
	"github.com/veloci/auth/sessions"
	"github.com/veloci/auth/store"
)

// stubTokenDB implements sessionStore and inviteStore for tests.
type stubTokenDB struct {
	stored    map[string]*store.TokenRow
	invites   map[string]*store.InviteTokenRow
	rotateErr error
}

func newStubTokenDB() *stubTokenDB {
	return &stubTokenDB{
		stored:  map[string]*store.TokenRow{},
		invites: map[string]*store.InviteTokenRow{},
	}
}

func (s *stubTokenDB) StoreToken(_ context.Context, id, userID, jti string, claims json.RawMessage, exp time.Time, tokenType string, parentID *string) error {
	s.stored[jti] = &store.TokenRow{CredentialID: userID, Claims: claims, ExpiresAt: exp, TokenType: tokenType}
	return nil
}

func (s *stubTokenDB) FindToken(_ context.Context, jti string) (*store.TokenRow, error) {
	row, ok := s.stored[jti]
	if !ok {
		return nil, sessions.ErrNotFound
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
	if row, ok := s.stored[oldJTI]; ok {
		now := time.Now()
		row.RotatedAt = &now
	}
	return nil
}

func (s *stubTokenDB) FindInviteToken(_ context.Context, tokenHash string) (*store.InviteTokenRow, error) {
	row, ok := s.invites[tokenHash]
	if !ok {
		return nil, sessions.ErrNotFound
	}
	return row, nil
}

func (s *stubTokenDB) StoreInviteToken(_ context.Context, tokenHash, createdBy string, claims []byte, expiresAt time.Time) error {
	s.invites[tokenHash] = &store.InviteTokenRow{
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

func newTestHandlers() (*sessions.Handler, *invites.Handler, *stubTokenDB) {
	db := newStubTokenDB()
	cfg := sessions.Config{AccessTTL: 15 * time.Minute, RefreshTTL: 24 * time.Hour}
	sess := sessions.NewHandler(db, testSecret, cfg)
	inv := invites.NewHandler(db, invites.Config{TTL: 72 * time.Hour})
	return sess, inv, db
}

func tokenRouter(sess *sessions.Handler, inv *invites.Handler) *chi.Mux {
	r := chi.NewRouter()
	api := humachi.New(r, huma.DefaultConfig("test", "1.0.0"))
	sessions.RegisterRoutes(api, sess)
	invites.RegisterRoutes(api, inv)
	return r
}

func mintToken(t *testing.T, r *chi.Mux, credentialID string) map[string]any {
	t.Helper()
	body, _ := json.Marshal(map[string]any{
		"credential_id": credentialID,
		"claims":        map[string]string{"sub": "user-1", "entity_id": "ent-1"},
	})
	req := httptest.NewRequest(http.MethodPost, "/tokens/mint", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusCreated {
		t.Fatalf("mint: got %d; body: %s", w.Code, w.Body)
	}
	var resp map[string]any
	json.NewDecoder(w.Body).Decode(&resp)
	return resp
}

func TestMintAndValidateToken(t *testing.T) {
	sess, inv, _ := newTestHandlers()
	r := tokenRouter(sess, inv)
	mintResp := mintToken(t, r, "cred-1")

	accessTok, _ := mintResp["access_token"].(string)
	if accessTok == "" {
		t.Fatal("expected access_token in mint response")
	}

	validateBody, _ := json.Marshal(map[string]string{"token": accessTok})
	req := httptest.NewRequest(http.MethodPost, "/tokens/validate", bytes.NewReader(validateBody))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("validate status: got %d; body: %s", w.Code, w.Body)
	}
	var validateResp map[string]any
	json.NewDecoder(w.Body).Decode(&validateResp)
	if validateResp["credential_id"] != "cred-1" {
		t.Errorf("credential_id: got %v", validateResp["credential_id"])
	}
	if validateResp["token_type"] != "access" {
		t.Errorf("token_type: got %v want access", validateResp["token_type"])
	}
}

func TestMint_NilClaimsRejected(t *testing.T) {
	sess, inv, _ := newTestHandlers()
	r := tokenRouter(sess, inv)

	body := []byte(`{"credential_id":"cred-1","claims":null}`)
	req := httptest.NewRequest(http.MethodPost, "/tokens/mint", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code < 400 {
		t.Errorf("expected error status for null claims, got %d; body: %s", w.Code, w.Body)
	}
}

func TestValidate_UserNotFound(t *testing.T) {
	sess, inv, db := newTestHandlers()
	r := tokenRouter(sess, inv)
	mintResp := mintToken(t, r, "cred-1")
	accessTok, _ := mintResp["access_token"].(string)

	for k := range db.stored {
		delete(db.stored, k)
	}

	validateBody, _ := json.Marshal(map[string]string{"token": accessTok})
	req := httptest.NewRequest(http.MethodPost, "/tokens/validate", bytes.NewReader(validateBody))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusUnauthorized {
		t.Errorf("expected 401 for missing DB row, got %d", w.Code)
	}
}

func TestRevokeAndValidate(t *testing.T) {
	sess, inv, _ := newTestHandlers()
	r := tokenRouter(sess, inv)
	mintResp := mintToken(t, r, "cred-1")

	accessTok, _ := mintResp["access_token"].(string)
	jti, _ := mintResp["jti"].(string)

	req := httptest.NewRequest(http.MethodDelete, "/tokens/"+jti, nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusNoContent {
		t.Fatalf("revoke: got %d; body: %s", w.Code, w.Body)
	}

	validateBody, _ := json.Marshal(map[string]string{"token": accessTok})
	req2 := httptest.NewRequest(http.MethodPost, "/tokens/validate", bytes.NewReader(validateBody))
	req2.Header.Set("Content-Type", "application/json")
	w2 := httptest.NewRecorder()
	r.ServeHTTP(w2, req2)
	if w2.Code != http.StatusUnauthorized {
		t.Errorf("expected 401 after revoke, got %d", w2.Code)
	}
}

func TestValidate_ExpiredDBRecord(t *testing.T) {
	sess, inv, db := newTestHandlers()
	r := tokenRouter(sess, inv)
	mintResp := mintToken(t, r, "cred-1")

	jti, _ := mintResp["jti"].(string)
	accessTok, _ := mintResp["access_token"].(string)

	if row, ok := db.stored[jti]; ok {
		row.ExpiresAt = time.Now().Add(-time.Minute)
	}

	validateBody, _ := json.Marshal(map[string]string{"token": accessTok})
	req := httptest.NewRequest(http.MethodPost, "/tokens/validate", bytes.NewReader(validateBody))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusUnauthorized {
		t.Errorf("expected 401 for expired DB record, got %d", w.Code)
	}
}

func TestValidate_InviteToken(t *testing.T) {
	sess, inv, _ := newTestHandlers()
	r := tokenRouter(sess, inv)

	body, _ := json.Marshal(map[string]any{
		"created_by": "cred-admin",
		"claims":     map[string]string{"email": "invited@example.com", "entity_id": "ent-1"},
	})
	req := httptest.NewRequest(http.MethodPost, "/invite", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusCreated {
		t.Fatalf("create invite: got %d; body: %s", w.Code, w.Body)
	}
	var invResp map[string]string
	json.NewDecoder(w.Body).Decode(&invResp)
	rawToken := invResp["token"]
	if rawToken == "" {
		t.Fatal("expected token in invite response")
	}

	validateBody, _ := json.Marshal(map[string]string{"token": rawToken})
	req2 := httptest.NewRequest(http.MethodPost, "/tokens/validate", bytes.NewReader(validateBody))
	req2.Header.Set("Content-Type", "application/json")
	w2 := httptest.NewRecorder()
	r.ServeHTTP(w2, req2)

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
	sess, inv, _ := newTestHandlers()
	r := tokenRouter(sess, inv)

	body, _ := json.Marshal(map[string]any{
		"created_by": "cred-admin",
		"claims":     map[string]string{"email": "invited@example.com"},
	})
	req := httptest.NewRequest(http.MethodPost, "/invite", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	var invResp map[string]string
	json.NewDecoder(w.Body).Decode(&invResp)
	rawToken := invResp["token"]

	consume := func() int {
		consumeBody, _ := json.Marshal(map[string]string{"token": rawToken})
		req := httptest.NewRequest(http.MethodPost, "/invite/consume", bytes.NewReader(consumeBody))
		req.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)
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
	sess, inv, db := newTestHandlers()
	r := tokenRouter(sess, inv)

	body, _ := json.Marshal(map[string]any{
		"created_by": "cred-admin",
		"claims":     map[string]string{"email": "invited@example.com"},
	})
	req := httptest.NewRequest(http.MethodPost, "/invite", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	var invResp map[string]string
	json.NewDecoder(w.Body).Decode(&invResp)
	rawToken := invResp["token"]

	hash := sha256hex(rawToken)
	if row, ok := db.invites[hash]; ok {
		row.ExpiresAt = time.Now().Add(-time.Minute)
	}

	consumeBody, _ := json.Marshal(map[string]string{"token": rawToken})
	req2 := httptest.NewRequest(http.MethodPost, "/invite/consume", bytes.NewReader(consumeBody))
	req2.Header.Set("Content-Type", "application/json")
	w2 := httptest.NewRecorder()
	r.ServeHTTP(w2, req2)
	if w2.Code != http.StatusGone {
		t.Errorf("expected 410 for expired invite, got %d", w2.Code)
	}
}
