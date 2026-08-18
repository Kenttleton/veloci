package store

import (
	"context"
	"time"

	"github.com/jackc/pgx/v5"
)

// Name holds a user's legal/formal name.
type Name struct {
	First string
	Last  string
}

// User represents a row from the users table joined with entity_users.
type User struct {
	ID            string    `db:"id"`
	Email         string    `db:"email"`
	FirstName     string    `db:"first_name"`
	LastName      string    `db:"last_name"`
	PreferredName string    `db:"preferred_name"`
	EntityRole    string    `db:"entity_role"`
	CreatedAt     time.Time `db:"created_at"`
}

// DisplayName returns PreferredName if set, otherwise falls back to
// "First Last", and finally to email if both are blank.
func (u User) DisplayName() string {
	if u.PreferredName != "" {
		return u.PreferredName
	}
	if u.FirstName != "" || u.LastName != "" {
		return u.FirstName + " " + u.LastName
	}
	return u.Email
}

// GetName returns the structured Name for the user.
func (u User) GetName() Name {
	return Name{First: u.FirstName, Last: u.LastName}
}

const userCols = `u.id::text, u.email, u.first_name, u.last_name, u.preferred_name, eu.entity_role, u.created_at`

// GetUserByID fetches a single user by user id within an entity.
func (s *Store) GetUserByID(ctx context.Context, entityID, userID string) (User, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT `+userCols+`
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
		SELECT `+userCols+`
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
		SELECT `+userCols+`
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

// UpdateUserProfile updates the name fields for the current user.
func (s *Store) UpdateUserProfile(ctx context.Context, userID, firstName, lastName, preferredName string) error {
	_, err := s.pool.Exec(ctx,
		`UPDATE users SET first_name = $2, last_name = $3, preferred_name = $4 WHERE id = $1`,
		userID, firstName, lastName, preferredName,
	)
	return err
}

// UpdateUserEmail updates the email for a user in the app DB.
func (s *Store) UpdateUserEmail(ctx context.Context, userID, email string) error {
	_, err := s.pool.Exec(ctx, `UPDATE users SET email = $2 WHERE id = $1`, userID, email)
	return err
}

// UpdateUserRole updates the entity_role for a user within an entity.
func (s *Store) UpdateUserRole(ctx context.Context, userID, entityID, role string) error {
	tag, err := s.pool.Exec(ctx,
		`UPDATE entity_users SET entity_role = $3 WHERE user_id = $1 AND entity_id = $2`,
		userID, entityID, role,
	)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return pgx.ErrNoRows
	}
	return nil
}

// DeleteUser removes a user from the entity.
func (s *Store) DeleteUser(ctx context.Context, entityID, userID string) error {
	_, err := s.pool.Exec(ctx,
		`DELETE FROM entity_users WHERE entity_id = $1 AND user_id = $2`,
		entityID, userID,
	)
	return err
}

// GetUserCredentialID returns the auth_credential_id for a user by user_id.
func (s *Store) GetUserCredentialID(ctx context.Context, userID string) (string, error) {
	var credentialID string
	err := s.pool.QueryRow(ctx,
		`SELECT auth_credential_id::text FROM users WHERE id = $1`,
		userID,
	).Scan(&credentialID)
	if err != nil {
		return "", err
	}
	return credentialID, nil
}

// EnsureUser inserts a user row if it does not already exist, returning the user id.
// On email conflict it updates the auth_credential_id so a credential reset is reflected.
func (s *Store) EnsureUser(ctx context.Context, email, credentialID string) (string, error) {
	var id string
	err := s.pool.QueryRow(ctx, `
		INSERT INTO users (id, email, auth_credential_id, created_at)
		VALUES (gen_random_uuid(), $1, $2::uuid, NOW())
		ON CONFLICT (email) DO UPDATE SET auth_credential_id = EXCLUDED.auth_credential_id
		RETURNING id::text
	`, email, credentialID).Scan(&id)
	return id, err
}
