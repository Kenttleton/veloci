package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/spf13/cobra"
	"github.com/spf13/viper"
	"github.com/veloci/auth/internal/db"
	"github.com/veloci/auth/internal/handlers"
	authsync "github.com/veloci/auth/internal/sync"
)

// dbCredAdapter adapts *db.DB to satisfy handlers.credentialStore.
type dbCredAdapter struct{ d *db.DB }

func (a *dbCredAdapter) FindCredentialByEmail(ctx context.Context, email string) (*handlers.CredentialRow, error) {
	c, err := a.d.FindCredentialByEmail(ctx, email)
	if err != nil {
		return nil, err
	}
	return &handlers.CredentialRow{ID: c.ID, PasswordHash: c.PasswordHash, SystemRole: c.SystemRole}, nil
}

func (a *dbCredAdapter) CreateCredential(ctx context.Context, id, email, hash, role string) error {
	return a.d.CreateCredential(ctx, id, email, hash, role)
}

// dbTokenAdapter adapts *db.DB to satisfy handlers.tokenStore.
type dbTokenAdapter struct{ d *db.DB }

func (a *dbTokenAdapter) StoreToken(ctx context.Context, id, userID, jti string, claims json.RawMessage, exp time.Time) error {
	return a.d.StoreToken(ctx, id, userID, jti, claims, exp)
}

func (a *dbTokenAdapter) FindToken(ctx context.Context, jti string) (*handlers.TokenRow, error) {
	row, err := a.d.FindToken(ctx, jti)
	if err != nil {
		return nil, err
	}
	return &handlers.TokenRow{CredentialID: row.CredentialID, Claims: row.Claims, ExpiresAt: row.ExpiresAt}, nil
}

func (a *dbTokenAdapter) DeleteToken(ctx context.Context, jti string) error {
	return a.d.DeleteToken(ctx, jti)
}

func main() {
	if err := rootCmd.Execute(); err != nil {
		log.Fatal(err)
	}
}

var rootCmd = &cobra.Command{Use: "veloci-auth", Short: "Veloci auth service"}

var serveCmd = &cobra.Command{
	Use:  "serve",
	RunE: runServe,
}

func init() {
	rootCmd.AddCommand(serveCmd)
	viper.AutomaticEnv()
	viper.SetDefault("PORT", "8081")
	// Env var overrides for secrets — AutomaticEnv doesn't map nested keys.
	// VELOCI_SERVER_ADMIN_EMAIL / VELOCI_SERVER_ADMIN_PASSWORD / VELOCI_JWT_SECRET
	// take precedence over veloci-auth.yaml values when set.
	viper.BindEnv("server_admin.email", "VELOCI_SERVER_ADMIN_EMAIL")       //nolint:errcheck
	viper.BindEnv("server_admin.password", "VELOCI_SERVER_ADMIN_PASSWORD") //nolint:errcheck
	viper.BindEnv("jwt_secret", "VELOCI_JWT_SECRET")                       //nolint:errcheck
}

func runServe(_ *cobra.Command, _ []string) error {
	configPath := viper.GetString("CONFIG_PATH")
	if configPath != "" {
		viper.SetConfigFile(configPath)
		if err := viper.ReadInConfig(); err != nil {
			return fmt.Errorf("config: %w", err)
		}
	}

	ctx := context.Background()
	database, err := db.New(ctx, viper.GetString("DATABASE_URL"))
	if err != nil {
		return fmt.Errorf("db: %w", err)
	}

	adminEmail := viper.GetString("server_admin.email")
	adminPass := viper.GetString("server_admin.password")
	if adminEmail != "" && adminPass != "" {
		if err := authsync.SyncServerAdmin(ctx, database, adminEmail, adminPass); err != nil {
			return fmt.Errorf("admin sync: %w", err)
		}
	}

	secret := []byte(viper.GetString("jwt_secret"))
	if len(secret) < 32 {
		return fmt.Errorf("jwt_secret must be at least 32 characters")
	}

	creds := handlers.NewCredentials(&dbCredAdapter{database})
	toks := handlers.NewTokens(&dbTokenAdapter{database}, secret)

	r := chi.NewRouter()
	r.Post("/credentials/validate", creds.Validate)
	r.Post("/credentials/create", creds.Create)
	r.Post("/tokens/mint", toks.Mint)
	r.Post("/tokens/validate", toks.Validate)
	r.Delete("/tokens/{jti}", toks.Revoke)

	port := viper.GetString("PORT")
	log.Printf("veloci-auth listening on :%s", port)
	return http.ListenAndServe(":"+port, r)
}
