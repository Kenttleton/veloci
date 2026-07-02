package sync

import (
	"context"
	"log"

	"github.com/google/uuid"
	"github.com/veloci/auth/internal/db"
	"golang.org/x/crypto/bcrypt"
)

// SyncServerAdmin upserts a server_admin credential for the given email/password.
// It is safe to call on every startup — the upsert is idempotent by email.
func SyncServerAdmin(ctx context.Context, d *db.DB, email, password string) error {
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
