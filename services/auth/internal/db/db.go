package db

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
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

// TokenRow holds the stored token fields returned from tokens.
type TokenRow struct {
	CredentialID string
	Claims       json.RawMessage
	ExpiresAt    time.Time
	TokenType    string
	RotatedAt    *time.Time
}

// InviteTokenRow holds fields from invite_tokens.
type InviteTokenRow struct {
	ID         string
	TokenHash  string
	CreatedBy  string
	Claims     json.RawMessage
	ExpiresAt  time.Time
	AcceptedAt *time.Time
}

// New creates a DB from the given DSN. It does not verify connectivity — call Ping.
func New(ctx context.Context, dsn string) (*DB, error) {
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		return nil, err
	}
	return &DB{pool: pool}, nil
}

// Ping verifies that the DB connection is alive.
func (d *DB) Ping(ctx context.Context) error {
	return d.pool.Ping(ctx)
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

// FindAdminCredential returns the credential row for the given email filtered to system_role = 'server_admin'.
func (d *DB) FindAdminCredential(ctx context.Context, email string) (*Credential, error) {
	c := new(Credential)
	err := d.pool.QueryRow(ctx,
		`SELECT id, password_hash, system_role FROM auth_credentials WHERE email = $1 AND system_role = 'server_admin'`,
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

// UpdateCredentialPassword updates the password hash for an existing credential by ID.
// Returns found=false (and no error) when the row does not exist.
func (d *DB) UpdateCredentialPassword(ctx context.Context, id, hash string) (found bool, err error) {
	tag, err := d.pool.Exec(ctx,
		`UPDATE auth_credentials SET password_hash = $2 WHERE id = $1`,
		id, hash,
	)
	if err != nil {
		return false, err
	}
	return tag.RowsAffected() > 0, nil
}

// DeleteCredential removes a credential by ID. It returns:
//   - found=false when the row does not exist
//   - systemRoleBlocked=true when the row is a server_admin (operation not permitted)
func (d *DB) DeleteCredential(ctx context.Context, id string) (found bool, systemRoleBlocked bool, err error) {
	var role string
	err = d.pool.QueryRow(ctx,
		`SELECT system_role FROM auth_credentials WHERE id = $1`,
		id,
	).Scan(&role)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, false, nil
	}
	if err != nil {
		return false, false, err
	}
	if role == "server_admin" {
		return true, true, nil
	}
	_, err = d.pool.Exec(ctx, `DELETE FROM auth_credentials WHERE id = $1`, id)
	if err != nil {
		return false, false, err
	}
	return true, false, nil
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

// StoreToken persists a minted token record with token type and optional parent reference.
// parentID may be nil for the first access token in a session.
func (d *DB) StoreToken(ctx context.Context, id, userID, jti string, claims json.RawMessage, expiresAt time.Time, tokenType string, parentID *string) error {
	_, err := d.pool.Exec(ctx,
		`INSERT INTO tokens (id, user_id, jti, claims, expires_at, token_type, parent_id)
		 VALUES ($1,$2,$3,$4,$5,$6,$7)`,
		id, userID, jti, claims, expiresAt, tokenType, parentID,
	)
	return err
}

// FindToken returns the token row for the given jti.
func (d *DB) FindToken(ctx context.Context, jti string) (*TokenRow, error) {
	row := new(TokenRow)
	err := d.pool.QueryRow(ctx,
		`SELECT user_id, claims, expires_at, token_type, rotated_at FROM tokens WHERE jti = $1`,
		jti,
	).Scan(&row.CredentialID, &row.Claims, &row.ExpiresAt, &row.TokenType, &row.RotatedAt)
	if err != nil {
		return nil, err
	}
	return row, nil
}

// DeleteToken removes a token record by jti (revocation). Always succeeds even if the jti is unknown.
func (d *DB) DeleteToken(ctx context.Context, jti string) error {
	_, err := d.pool.Exec(ctx, `DELETE FROM tokens WHERE jti = $1`, jti)
	return err
}

// DeleteUserTokens removes all token records for a given credential ID.
func (d *DB) DeleteUserTokens(ctx context.Context, credentialID string) error {
	_, err := d.pool.Exec(ctx, `DELETE FROM tokens WHERE user_id = $1`, credentialID)
	return err
}

// RotateRefreshToken stamps rotated_at on the old refresh token row within a transaction.
// It enforces the replay window: if rotated_at is already set and older than graceWindow, it
// returns ErrReplayDetected. The caller is responsible for inserting the new token pair after
// this call succeeds.
func (d *DB) RotateRefreshToken(ctx context.Context, oldJTI string, graceWindow time.Duration) error {
	tx, err := d.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin tx: %w", err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck

	var rotatedAt *time.Time
	var rowID string
	err = tx.QueryRow(ctx,
		`SELECT id, rotated_at FROM tokens WHERE jti = $1 FOR UPDATE`,
		oldJTI,
	).Scan(&rowID, &rotatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrTokenNotFound
	}
	if err != nil {
		return fmt.Errorf("find refresh token: %w", err)
	}

	if rotatedAt != nil {
		if time.Since(*rotatedAt) > graceWindow {
			return ErrReplayDetected
		}
		// Within grace window — already rotated but within tolerance; allow
	} else {
		// First rotation — stamp rotated_at
		_, err = tx.Exec(ctx, `UPDATE tokens SET rotated_at = NOW() WHERE id = $1`, rowID)
		if err != nil {
			return fmt.Errorf("stamp rotated_at: %w", err)
		}
	}

	return tx.Commit(ctx)
}

// ErrTokenNotFound is returned when a token row is not found by JTI.
var ErrTokenNotFound = errors.New("token not found")

// ErrReplayDetected is returned when a refresh token is presented outside the grace window.
var ErrReplayDetected = errors.New("refresh token replay detected")

// StoreInviteToken persists a new invite token record.
func (d *DB) StoreInviteToken(ctx context.Context, tokenHash, createdBy string, claims []byte, expiresAt time.Time) error {
	_, err := d.pool.Exec(ctx,
		`INSERT INTO invite_tokens (token_hash, created_by, claims, expires_at)
		 VALUES ($1,$2,$3,$4)`,
		tokenHash, createdBy, claims, expiresAt,
	)
	return err
}

// FindInviteToken retrieves an invite token row by its hash.
func (d *DB) FindInviteToken(ctx context.Context, tokenHash string) (*InviteTokenRow, error) {
	row := new(InviteTokenRow)
	err := d.pool.QueryRow(ctx,
		`SELECT id, token_hash, created_by, claims, expires_at, accepted_at
		 FROM invite_tokens WHERE token_hash = $1`,
		tokenHash,
	).Scan(&row.ID, &row.TokenHash, &row.CreatedBy, &row.Claims, &row.ExpiresAt, &row.AcceptedAt)
	if err != nil {
		return nil, err
	}
	return row, nil
}

// ConsumeInviteToken atomically marks an invite token as consumed.
// Returns:
//   - found=false when no row matches the hash
//   - alreadyConsumed=true when accepted_at is already set
//   - expired=true when expires_at is in the past (but accepted_at is null)
func (d *DB) ConsumeInviteToken(ctx context.Context, tokenHash string) (found bool, alreadyConsumed bool, expired bool, err error) {
	// Read the current row first to distinguish states.
	row := new(InviteTokenRow)
	err = d.pool.QueryRow(ctx,
		`SELECT id, expires_at, accepted_at FROM invite_tokens WHERE token_hash = $1`,
		tokenHash,
	).Scan(&row.ID, &row.ExpiresAt, &row.AcceptedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, false, false, nil
	}
	if err != nil {
		return false, false, false, err
	}

	if row.AcceptedAt != nil {
		return true, true, false, nil
	}
	if time.Now().After(row.ExpiresAt) {
		return true, false, true, nil
	}

	// Atomically consume: UPDATE WHERE accepted_at IS NULL ensures only one winner.
	tag, err := d.pool.Exec(ctx,
		`UPDATE invite_tokens SET accepted_at = NOW() WHERE id = $1 AND accepted_at IS NULL`,
		row.ID,
	)
	if err != nil {
		return true, false, false, err
	}
	if tag.RowsAffected() == 0 {
		// A concurrent request won the race.
		return true, true, false, nil
	}
	return true, false, false, nil
}
