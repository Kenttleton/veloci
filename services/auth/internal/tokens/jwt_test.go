package tokens_test

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/veloci/auth/internal/tokens"
)

func TestMintAndVerify(t *testing.T) {
	secret := []byte("test-secret-at-least-32-characters!!")
	claims := json.RawMessage(`{"sub":"user-1","entity_id":"ent-1","entity_role":"entity_admin"}`)
	jti := "test-jti-1"
	exp := time.Now().Add(time.Hour)

	tok, err := tokens.Mint(secret, jti, claims, exp)
	if err != nil {
		t.Fatalf("Mint: %v", err)
	}

	gotJTI, gotClaims, err := tokens.Verify(secret, tok)
	if err != nil {
		t.Fatalf("Verify: %v", err)
	}
	if gotJTI != jti {
		t.Errorf("jti: got %q want %q", gotJTI, jti)
	}

	var m map[string]any
	json.Unmarshal(gotClaims, &m)
	if m["sub"] != "user-1" {
		t.Errorf("sub: got %v want user-1", m["sub"])
	}
}

func TestVerifyRejectsExpired(t *testing.T) {
	secret := []byte("test-secret-at-least-32-characters!!")
	tok, _ := tokens.Mint(secret, "j", json.RawMessage(`{}`), time.Now().Add(-time.Minute))
	_, _, err := tokens.Verify(secret, tok)
	if err == nil {
		t.Error("expected error for expired token")
	}
}

func TestVerifyRejectsWrongSecret(t *testing.T) {
	tok, _ := tokens.Mint([]byte("secret-a-at-least-32-characters!!"), "j", json.RawMessage(`{}`), time.Now().Add(time.Hour))
	_, _, err := tokens.Verify([]byte("secret-b-at-least-32-characters!!"), tok)
	if err == nil {
		t.Error("expected error for wrong secret")
	}
}
