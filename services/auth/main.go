package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"
	"unicode"

	"github.com/cenkalti/backoff/v4"
	"github.com/danielgtaylor/huma/v2"
	"github.com/danielgtaylor/huma/v2/adapters/humachi"
	"github.com/go-chi/chi/v5"
	"github.com/spf13/cobra"
	"github.com/spf13/viper"
	"github.com/veloci/auth/admin"
	"github.com/veloci/auth/credentials"
	"github.com/veloci/auth/invites"
	"github.com/veloci/auth/sessions"
	"github.com/veloci/auth/store"
)

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
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

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

	database, err := store.New(ctx, dsn)
	if err != nil {
		return fmt.Errorf("db: %w", err)
	}

	bo := backoff.NewExponentialBackOff()
	bo.InitialInterval = 500 * time.Millisecond
	bo.MaxInterval = 30 * time.Second
	bo.MaxElapsedTime = 2 * time.Minute

	pingErr := backoff.RetryNotify(
		func() error { return database.Ping(ctx) },
		backoff.WithContext(bo, ctx),
		func(err error, d time.Duration) {
			log.Printf("db not ready (retrying in %s): %v", d.Round(time.Millisecond), err)
		},
	)
	if pingErr != nil {
		return fmt.Errorf("db unreachable after 2 minutes: %w", pingErr)
	}

	adminEmail := viper.GetString("auth.admin.email")
	adminPass := viper.GetString("auth.admin.password")
	if adminEmail != "" && adminPass != "" {
		if err := admin.SyncServerAdmin(ctx, database, adminEmail, adminPass); err != nil {
			return fmt.Errorf("admin sync: %w", err)
		}
	}

	secret := []byte(viper.GetString("auth.jwt_secret"))
	if len(secret) < 32 {
		return fmt.Errorf("auth.jwt_secret must be at least 32 characters")
	}
	warnWeakSecret(string(secret))

	tokenCfg := sessions.Config{
		AccessTTL:  time.Duration(viper.GetInt("session.access_token_ttl_minutes")) * time.Minute,
		RefreshTTL: time.Duration(viper.GetInt("session.refresh_token_ttl_hours")) * time.Hour,
	}
	if tokenCfg.AccessTTL <= 0 {
		tokenCfg.AccessTTL = 15 * time.Minute
	}
	if tokenCfg.RefreshTTL <= 0 {
		tokenCfg.RefreshTTL = 24 * time.Hour
	}

	inviteCfg := invites.Config{
		TTL: time.Duration(viper.GetInt("invite.ttl_hours")) * time.Hour,
	}
	if inviteCfg.TTL <= 0 {
		inviteCfg.TTL = 72 * time.Hour
	}

	// *store.DB satisfies all domain store interfaces directly — no adapters needed.
	creds := credentials.NewHandler(database)
	toks := sessions.NewHandler(database, secret, tokenCfg)
	inv := invites.NewHandler(database, inviteCfg)

	r := chi.NewRouter()
	api := humachi.New(r, huma.DefaultConfig("Veloci Auth", "1.0.0"))

	credentials.RegisterRoutes(api, creds)
	sessions.RegisterRoutes(api, toks)
	invites.RegisterRoutes(api, inv)

	r.Get("/openapi.json", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		if err := json.NewEncoder(w).Encode(api.OpenAPI()); err != nil {
			http.Error(w, "failed to encode spec", http.StatusInternalServerError)
		}
	})

	port := viper.GetInt("auth.port")
	srv := &http.Server{
		Addr:    fmt.Sprintf(":%d", port),
		Handler: r,
	}

	go func() {
		<-ctx.Done()
		log.Printf("veloci-auth: shutting down gracefully")
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := srv.Shutdown(shutdownCtx); err != nil {
			log.Printf("veloci-auth: shutdown error: %v", err)
		}
	}()

	log.Printf("veloci-auth listening on :%d", port)
	if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		return err
	}
	return nil
}

func warnWeakSecret(secret string) {
	low := strings.ToLower(secret)
	placeholders := []string{"changeme", "replace", "your-secret", "example"}
	for _, p := range placeholders {
		if strings.Contains(low, p) {
			log.Printf("WARNING: jwt_secret contains placeholder text %q — rotate before exposing to a network", p)
			return
		}
	}
	allLowerAlpha := true
	for _, c := range secret {
		if !unicode.IsLower(c) || unicode.IsDigit(c) {
			allLowerAlpha = false
			break
		}
	}
	if allLowerAlpha {
		log.Printf("WARNING: jwt_secret appears low-entropy (all lowercase letters, no digits or symbols) — rotate before exposing to a network")
	}
}
