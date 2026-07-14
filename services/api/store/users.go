package store

import (
	"context"
	"time"

	"github.com/jackc/pgx/v5"
)

// User represents a row from the users table joined with entity_users.
type User struct {
	ID         string    `db:"id"`
	Email      string    `db:"email"`
	EntityRole string    `db:"entity_role"`
	CreatedAt  time.Time `db:"created_at"`
}

// GetUserByID fetches a single user by user id within an entity.
func (s *Store) GetUserByID(ctx context.Context, entityID, userID string) (User, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT u.id::text, u.email, eu.entity_role, u.created_at
		FROM users u
		JOIN entity_users eu ON eu.user_id = u.id
		WHERE eu.entity_id = $1 AND u.id = $2
	`, entityID, userID)
	if err != nil {
		return User{}, err
	}
	return pgx.CollectOneRow(rows, pgx.RowToStructByName[User])
}

// GetUserByEmail fetches a user by email within an entity.
func (s *Store) GetUserByEmail(ctx context.Context, entityID, email string) (User, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT u.id::text, u.email, eu.entity_role, u.created_at
		FROM users u
		JOIN entity_users eu ON eu.user_id = u.id
		WHERE eu.entity_id = $1 AND u.email = $2
	`, entityID, email)
	if err != nil {
		return User{}, err
	}
	return pgx.CollectOneRow(rows, pgx.RowToStructByName[User])
}

// ListUsers returns all users belonging to an entity.
func (s *Store) ListUsers(ctx context.Context, entityID string) ([]User, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT u.id::text, u.email, eu.entity_role, u.created_at
		FROM users u
		JOIN entity_users eu ON eu.user_id = u.id
		WHERE eu.entity_id = $1
		ORDER BY u.created_at ASC
	`, entityID)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, pgx.RowToStructByName[User])
}

// UpdateUserProfile updates display fields on the current user (noop stub; schema extension point).
func (s *Store) UpdateUserProfile(ctx context.Context, userID string) error {
	_, err := s.pool.Exec(ctx, `UPDATE users SET id = id WHERE id = $1`, userID)
	return err
}

// DeleteUser removes a user from the entity.
func (s *Store) DeleteUser(ctx context.Context, entityID, userID string) error {
	_, err := s.pool.Exec(ctx, `
		DELETE FROM entity_users WHERE entity_id = $1 AND user_id = $2
	`, entityID, userID)
	return err
}

// GetUserCredentialID returns the credential_id for a user by user_id.
func (s *Store) GetUserCredentialID(ctx context.Context, userID string) (string, error) {
	var credentialID string
	err := s.pool.QueryRow(ctx, `
		SELECT credential_id::text FROM users WHERE id = $1
	`, userID).Scan(&credentialID)
	if err != nil {
		return "", err
	}
	return credentialID, nil
}

// EnsureUser inserts a user row if it does not already exist, returning the user id.
func (s *Store) EnsureUser(ctx context.Context, email, credentialID string) (string, error) {
	var id string
	err := s.pool.QueryRow(ctx, `
		INSERT INTO users (id, email, credential_id, created_at)
		VALUES (gen_random_uuid(), $1, $2::uuid, NOW())
		ON CONFLICT (email) DO UPDATE SET email = EXCLUDED.email
		RETURNING id::text
	`, email, credentialID).Scan(&id)
	return id, err
}
