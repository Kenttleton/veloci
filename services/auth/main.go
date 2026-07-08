package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"strings"
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
	viper.SetEnvPrefix("VELOCI")
	viper.SetEnvKeyReplacer(strings.NewReplacer(".", "_"))
	viper.AutomaticEnv()
	viper.SetDefault("auth.port", 8081)
}

func runServe(_ *cobra.Command, _ []string) error {
	configPath := os.Getenv("VELOCI_CONFIG_PATH")
	if configPath == "" {
		configPath = "config/veloci.toml"
	}
	viper.SetConfigFile(configPath)
	viper.SetConfigType("toml")
	if err := viper.ReadInConfig(); err != nil {
		log.Printf("config: %v — using defaults and env vars", err)
	}

	dsn := fmt.Sprintf("postgres://%s:%s@%s:%d/%s?sslmode=disable",
		viper.GetString("database.auth.user"),
		viper.GetString("database.auth.password"),
		viper.GetString("database.host"),
		viper.GetInt("database.port"),
		viper.GetString("database.auth.name"),
	)
	ctx := context.Background()
	database, err := db.New(ctx, dsn)
	if err != nil {
		return fmt.Errorf("db: %w", err)
	}

	adminEmail := viper.GetString("auth.admin.email")
	adminPass := viper.GetString("auth.admin.password")
	if adminEmail != "" && adminPass != "" {
		if err := authsync.SyncServerAdmin(ctx, database, adminEmail, adminPass); err != nil {
			return fmt.Errorf("admin sync: %w", err)
		}
	}

	secret := []byte(viper.GetString("auth.jwt_secret"))
	if len(secret) < 32 {
		return fmt.Errorf("auth.jwt_secret must be at least 32 characters")
	}

	creds := handlers.NewCredentials(&dbCredAdapter{database})
	toks := handlers.NewTokens(&dbTokenAdapter{database}, secret)

	r := chi.NewRouter()
	r.Post("/credentials/validate", creds.Validate)
	r.Post("/credentials/create", creds.Create)
	r.Post("/tokens/mint", toks.Mint)
	r.Post("/tokens/validate", toks.Validate)
	r.Delete("/tokens/{jti}", toks.Revoke)

	port := viper.GetInt("auth.port")
	log.Printf("veloci-auth listening on :%d", port)
	return http.ListenAndServe(fmt.Sprintf(":%d", port), r)
}
