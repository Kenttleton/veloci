package admin

import (
	"context"
	"errors"
	"log"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/veloci/auth/store"
	"golang.org/x/crypto/bcrypt"
)

// SyncServerAdmin ensures a server_admin credential exists for the given email/password.
// On first run it hashes and inserts. On subsequent restarts it compares the config
// password against the stored hash — bcrypt work only runs when the password has changed.
// Changing the config password and restarting is the intentional admin-reset UX.
func SyncServerAdmin(ctx context.Context, d *store.DB, email, password string) error {
	existing, err := d.FindAdminCredential(ctx, email)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return err
	}
	if existing != nil {
		compareErr := bcrypt.CompareHashAndPassword([]byte(existing.PasswordHash), []byte(password))
		if compareErr == nil {
			log.Printf("sync: server_admin credential unchanged for %s", email)
			return nil
		}
		if !errors.Is(compareErr, bcrypt.ErrMismatchedHashAndPassword) {
			log.Printf("sync: server_admin hash comparison error for %s: %v", email, compareErr)
		}
		// password changed — fall through to rehash + upsert
	}

	hash, err := bcrypt.GenerateFromPassword([]byte(password), 12)
	if err != nil {
		return err
	}
	if err := d.UpsertCredential(ctx, uuid.New().String(), email, string(hash), "server_admin"); err != nil {
		return err
	}
	log.Printf("sync: server_admin credential synced for %s", email)
	return nil
}
