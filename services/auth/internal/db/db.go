package db

import (
	"context"
	"encoding/json"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// DB wraps a pgxpool.Pool providing auth-domain query methods.
type DB struct{ pool *pgxpool.Pool }

// Credential holds the stored credential fields returned from auth_credentials.
type Credential struct {
	ID           string
	PasswordHash string
	SystemRole   string
}

// New creates a DB from the given DSN, connecting immediately to verify connectivity.
func New(ctx context.Context, dsn string) (*DB, error) {
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		return nil, err
	}
	return &DB{pool: pool}, nil
}

// FindCredentialByEmail returns the credential row for the given email.
func (d *DB) FindCredentialByEmail(ctx context.Context, email string) (*Credential, error) {
	c := new(Credential)
	err := d.pool.QueryRow(ctx,
		`SELECT id, password_hash, system_role FROM auth_credentials WHERE email = $1`,
		email,
	).Scan(&c.ID, &c.PasswordHash, &c.SystemRole)
	if err != nil {
		return nil, err
	}
	return c, nil
}

// CreateCredential inserts a new credential row.
func (d *DB) CreateCredential(ctx context.Context, id, email, hash, role string) error {
	_, err := d.pool.Exec(ctx,
		`INSERT INTO auth_credentials (id, email, password_hash, system_role) VALUES ($1,$2,$3,$4)`,
		id, email, hash, role,
	)
	return err
}

// UpsertCredential inserts or updates a credential row by email.
func (d *DB) UpsertCredential(ctx context.Context, id, email, hash, role string) error {
	_, err := d.pool.Exec(ctx, `
		INSERT INTO auth_credentials (id, email, password_hash, system_role)
		VALUES ($1,$2,$3,$4)
		ON CONFLICT (email) DO UPDATE
		  SET password_hash = EXCLUDED.password_hash,
		      system_role   = EXCLUDED.system_role
	`, id, email, hash, role)
	return err
}

// StoreToken persists a minted token record.
func (d *DB) StoreToken(ctx context.Context, id, userID, jti string, claims json.RawMessage, expiresAt time.Time) error {
	_, err := d.pool.Exec(ctx,
		`INSERT INTO tokens (id, user_id, jti, claims, expires_at) VALUES ($1,$2,$3,$4,$5)`,
		id, userID, jti, claims, expiresAt,
	)
	return err
}

// TokenRow holds the stored token fields returned from tokens.
type TokenRow struct {
	CredentialID string
	Claims       json.RawMessage
	ExpiresAt    time.Time
}

// FindToken returns the token row for the given jti.
func (d *DB) FindToken(ctx context.Context, jti string) (*TokenRow, error) {
	row := new(TokenRow)
	err := d.pool.QueryRow(ctx,
		`SELECT user_id, claims, expires_at FROM tokens WHERE jti = $1`,
		jti,
	).Scan(&row.CredentialID, &row.Claims, &row.ExpiresAt)
	if err != nil {
		return nil, err
	}
	return row, nil
}

// DeleteToken removes a token record by jti (revocation).
func (d *DB) DeleteToken(ctx context.Context, jti string) error {
	_, err := d.pool.Exec(ctx, `DELETE FROM tokens WHERE jti = $1`, jti)
	return err
}
