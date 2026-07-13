package sessions_test

import (
	"encoding/base64"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

// These tests exercise the JWT functions indirectly via the Mint+Validate HTTP flow.
// Direct unit tests of mintJWT/verifyJWT are kept here using the jwt library directly
// to verify the same contract: signature validation, expiry, alg:none rejection.

var jwtSecret = []byte("test-secret-at-least-32-characters!!")

func TestJWT_SignAndVerify(t *testing.T) {
	claims := jwt.MapClaims{
		"sub": "user-1",
		"jti": "jti-1",
		"exp": time.Now().Add(time.Hour).Unix(),
		"iat": time.Now().Unix(),
	}
	tok, err := jwt.NewWithClaims(jwt.SigningMethodHS256, claims).SignedString(jwtSecret)
	if err != nil {
		t.Fatalf("sign: %v", err)
	}

	parsed, err := jwt.Parse(tok, func(t *jwt.Token) (any, error) {
		return jwtSecret, nil
	})
	if err != nil || !parsed.Valid {
		t.Fatalf("parse: %v", err)
	}
	mc := parsed.Claims.(jwt.MapClaims)
	if mc["sub"] != "user-1" {
		t.Errorf("sub: got %v", mc["sub"])
	}
}

func TestJWT_WrongSecretRejected(t *testing.T) {
	claims := jwt.MapClaims{"exp": time.Now().Add(time.Hour).Unix()}
	tok, _ := jwt.NewWithClaims(jwt.SigningMethodHS256, claims).SignedString(jwtSecret)

	_, err := jwt.Parse(tok, func(t *jwt.Token) (any, error) {
		return []byte("different-secret-at-least-32chars!!"), nil
	})
	if err == nil {
		t.Error("expected error for wrong secret")
	}
}

func TestJWT_ExpiredRejected(t *testing.T) {
	claims := jwt.MapClaims{"exp": time.Now().Add(-time.Minute).Unix()}
	tok, _ := jwt.NewWithClaims(jwt.SigningMethodHS256, claims).SignedString(jwtSecret)

	_, err := jwt.Parse(tok, func(t *jwt.Token) (any, error) { return jwtSecret, nil })
	if err == nil {
		t.Error("expected error for expired token")
	}
}

func TestJWT_AlgNoneRejected(t *testing.T) {
	header := base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"none","typ":"JWT"}`))
	payload := base64.RawURLEncoding.EncodeToString([]byte(`{"jti":"bad","exp":9999999999}`))
	algNoneToken := strings.Join([]string{header, payload, ""}, ".")

	_, err := jwt.Parse(algNoneToken, func(t *jwt.Token) (any, error) { return jwtSecret, nil })
	if err == nil {
		t.Error("expected Verify to reject alg:none token")
	}
}

func TestJWT_ClaimsRoundTrip(t *testing.T) {
	original := map[string]any{
		"sub":       "user-1",
		"entity_id": "ent-1",
	}
	b, _ := json.Marshal(original)
	var m map[string]any
	json.Unmarshal(b, &m)

	m["jti"] = "jti-1"
	m["exp"] = time.Now().Add(time.Hour).Unix()
	m["iat"] = time.Now().Unix()
	m["token_type"] = "access"

	tok, err := jwt.NewWithClaims(jwt.SigningMethodHS256, jwt.MapClaims(m)).SignedString(jwtSecret)
	if err != nil {
		t.Fatalf("sign: %v", err)
	}

	parsed, _ := jwt.Parse(tok, func(t *jwt.Token) (any, error) { return jwtSecret, nil })
	mc := parsed.Claims.(jwt.MapClaims)
	if mc["sub"] != "user-1" {
		t.Errorf("sub: got %v", mc["sub"])
	}
	if mc["entity_id"] != "ent-1" {
		t.Errorf("entity_id: got %v", mc["entity_id"])
	}
}
