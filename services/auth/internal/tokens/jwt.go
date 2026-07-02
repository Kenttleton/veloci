package tokens

import (
	"encoding/json"
	"fmt"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

// Mint signs a JWT. Claims are opaque — veloci-auth embeds them as-is.
// jti, iat, exp are added by this function; callers must not include them in claims.
func Mint(secret []byte, jti string, claims json.RawMessage, expiresAt time.Time) (string, error) {
	var m map[string]any
	if err := json.Unmarshal(claims, &m); err != nil {
		return "", fmt.Errorf("invalid claims JSON: %w", err)
	}
	m["jti"] = jti
	m["iat"] = time.Now().Unix()
	m["exp"] = expiresAt.Unix()
	return jwt.NewWithClaims(jwt.SigningMethodHS256, jwt.MapClaims(m)).SignedString(secret)
}

// Verify validates signature and expiry. Returns jti and the original claims
// (jti/iat/exp stripped). Does NOT check the token DB — that is the caller's job.
func Verify(secret []byte, tokenStr string) (jti string, claims json.RawMessage, err error) {
	token, err := jwt.Parse(tokenStr, func(t *jwt.Token) (any, error) {
		if _, ok := t.Method.(*jwt.SigningMethodHMAC); !ok {
			return nil, fmt.Errorf("unexpected signing method: %v", t.Header["alg"])
		}
		return secret, nil
	})
	if err != nil {
		return "", nil, err
	}
	mc, ok := token.Claims.(jwt.MapClaims)
	if !ok || !token.Valid {
		return "", nil, fmt.Errorf("invalid token")
	}
	jtiVal, _ := mc["jti"].(string)
	delete(mc, "jti")
	delete(mc, "iat")
	delete(mc, "exp")
	raw, err := json.Marshal(map[string]any(mc))
	return jtiVal, raw, err
}
