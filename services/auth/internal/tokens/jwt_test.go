package tokens_test

import (
	"encoding/base64"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/veloci/auth/internal/tokens"
)

func TestMintAndVerify(t *testing.T) {
	secret := []byte("test-secret-at-least-32-characters!!")
	claims := json.RawMessage(`{"sub":"user-1","entity_id":"ent-1","entity_role":"entity_admin"}`)
	jti := "test-jti-1"
	exp := time.Now().Add(time.Hour)

	tok, err := tokens.Mint(secret, jti, claims, exp, "access")
	if err != nil {
		t.Fatalf("Mint: %v", err)
	}

	gotJTI, gotType, gotClaims, err := tokens.Verify(secret, tok)
	if err != nil {
		t.Fatalf("Verify: %v", err)
	}
	if gotJTI != jti {
		t.Errorf("jti: got %q want %q", gotJTI, jti)
	}
	if gotType != "access" {
		t.Errorf("token_type: got %q want %q", gotType, "access")
	}

	var m map[string]any
	json.Unmarshal(gotClaims, &m)
	if m["sub"] != "user-1" {
		t.Errorf("sub: got %v want user-1", m["sub"])
	}
}

func TestVerifyRejectsExpired(t *testing.T) {
	secret := []byte("test-secret-at-least-32-characters!!")
	tok, _ := tokens.Mint(secret, "j", json.RawMessage(`{}`), time.Now().Add(-time.Minute), "access")
	_, _, _, err := tokens.Verify(secret, tok)
	if err == nil {
		t.Error("expected error for expired token")
	}
}

func TestVerifyRejectsWrongSecret(t *testing.T) {
	tok, _ := tokens.Mint([]byte("secret-a-at-least-32-characters!!"), "j", json.RawMessage(`{}`), time.Now().Add(time.Hour), "access")
	_, _, _, err := tokens.Verify([]byte("secret-b-at-least-32-characters!!"), tok)
	if err == nil {
		t.Error("expected error for wrong secret")
	}
}

// TestVerify_AlgNoneRejected crafts a token with alg:none and asserts Verify returns an error.
func TestVerify_AlgNoneRejected(t *testing.T) {
	// Build a fake JWT with alg:none manually — header.payload.emptysig
	header := base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"none","typ":"JWT"}`))
	payload := base64.RawURLEncoding.EncodeToString([]byte(`{"jti":"bad","exp":9999999999,"token_type":"access"}`))
	// alg:none tokens have an empty signature segment
	algNoneToken := strings.Join([]string{header, payload, ""}, ".")

	secret := []byte("test-secret-at-least-32-characters!!")
	_, _, _, err := tokens.Verify(secret, algNoneToken)
	if err == nil {
		t.Error("expected Verify to reject alg:none token, but got nil error")
	}
}
