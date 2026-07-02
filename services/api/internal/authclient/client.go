// Package authclient provides an HTTP client for the veloci-auth service.
package authclient

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
)

// Client communicates with the veloci-auth service.
type Client struct {
	baseURL    string
	httpClient *http.Client
}

// New creates a new Client targeting the given base URL.
func New(baseURL string) *Client {
	return &Client{baseURL: baseURL, httpClient: &http.Client{}}
}

// ValidateResult is returned by ValidateToken.
type ValidateResult struct {
	JTI          string          `json:"jti"`
	CredentialID string          `json:"credential_id"`
	Claims       json.RawMessage `json:"claims"`
}

// ValidateCredResult is returned by ValidateCredential.
type ValidateCredResult struct {
	CredentialID string `json:"credential_id"`
	SystemRole   string `json:"system_role"`
}

// MintResult is returned by MintToken.
type MintResult struct {
	Token     string `json:"token"`
	JTI       string `json:"jti"`
	ExpiresAt string `json:"expires_at"`
}

// ValidateToken validates a bearer token with veloci-auth.
func (c *Client) ValidateToken(ctx context.Context, token string) (*ValidateResult, error) {
	body, _ := json.Marshal(map[string]string{"token": token})
	resp, err := c.post(ctx, "/tokens/validate", body)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("auth: validate returned %d", resp.StatusCode)
	}
	var result ValidateResult
	return &result, json.NewDecoder(resp.Body).Decode(&result)
}

// ValidateCredential validates email/password credentials with veloci-auth.
func (c *Client) ValidateCredential(ctx context.Context, email, password string) (*ValidateCredResult, error) {
	body, _ := json.Marshal(map[string]string{"email": email, "password": password})
	resp, err := c.post(ctx, "/credentials/validate", body)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("auth: credential validate returned %d", resp.StatusCode)
	}
	var result ValidateCredResult
	return &result, json.NewDecoder(resp.Body).Decode(&result)
}

// MintToken mints a new token with the given credential ID and claims.
func (c *Client) MintToken(ctx context.Context, credentialID string, claims map[string]any) (*MintResult, error) {
	body, _ := json.Marshal(map[string]any{
		"credential_id": credentialID,
		"claims":        claims,
	})
	resp, err := c.post(ctx, "/tokens/mint", body)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		return nil, fmt.Errorf("auth: mint returned %d", resp.StatusCode)
	}
	var result MintResult
	return &result, json.NewDecoder(resp.Body).Decode(&result)
}

// RevokeToken revokes the token identified by jti.
func (c *Client) RevokeToken(ctx context.Context, jti string) error {
	req, _ := http.NewRequestWithContext(ctx, http.MethodDelete, c.baseURL+"/tokens/"+jti, nil)
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return err
	}
	resp.Body.Close()
	return nil
}

// CreateCredential creates a new email/password credential and returns the credential ID.
func (c *Client) CreateCredential(ctx context.Context, email, password string) (string, error) {
	body, _ := json.Marshal(map[string]string{"email": email, "password": password})
	resp, err := c.post(ctx, "/credentials/create", body)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		return "", fmt.Errorf("auth: create credential returned %d", resp.StatusCode)
	}
	var result map[string]string
	json.NewDecoder(resp.Body).Decode(&result)
	return result["credential_id"], nil
}

func (c *Client) post(ctx context.Context, path string, body []byte) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+path, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	return c.httpClient.Do(req)
}
