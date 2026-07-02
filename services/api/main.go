package main

import (
	"context"
	"fmt"
	"log"
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/spf13/cobra"
	"github.com/spf13/viper"

	"github.com/veloci/api/internal/authclient"
	"github.com/veloci/api/internal/handlers"
	"github.com/veloci/api/internal/middleware"
	"github.com/veloci/api/internal/queue"
)

type appDBImpl struct {
	pool *pgxpool.Pool
}

func (d *appDBImpl) FindUserEntity(ctx context.Context, email string) (handlers.UserEntity, error) {
	var ue handlers.UserEntity
	err := d.pool.QueryRow(ctx, `
		SELECT u.id::text, eu.entity_id::text, eu.entity_role
		FROM users u
		JOIN entity_users eu ON eu.user_id = u.id
		WHERE u.email = $1
		LIMIT 1
	`, email).Scan(&ue.UserID, &ue.EntityID, &ue.EntityRole)
	return ue, err
}

func main() {
	if err := rootCmd.Execute(); err != nil {
		log.Fatal(err)
	}
}

var rootCmd = &cobra.Command{Use: "veloci-api", Short: "Veloci API service"}

var serveCmd = &cobra.Command{Use: "serve", Short: "Start the HTTP server", RunE: runServe}

func init() {
	rootCmd.AddCommand(serveCmd)
	viper.AutomaticEnv()
	viper.SetDefault("PORT", "8080")
}

func runServe(_ *cobra.Command, _ []string) error {
	authURL := viper.GetString("VELOCI_AUTH_URL")
	if authURL == "" {
		return fmt.Errorf("VELOCI_AUTH_URL required")
	}

	pub, err := queue.NewPublisher(viper.GetString("RABBITMQ_URL"))
	if err != nil {
		return fmt.Errorf("queue: %w", err)
	}
	_ = pub // TODO: pass to financial route handlers in service implementation plan

	pool, err := pgxpool.New(context.Background(), viper.GetString("DATABASE_URL"))
	if err != nil {
		return fmt.Errorf("database: %w", err)
	}
	defer pool.Close()

	authClient := authclient.New(authURL)

	authHandler := handlers.NewAuth(authURL, &appDBImpl{pool: pool})

	r := chi.NewRouter()
	r.Get("/health", handlers.Health)
	r.Post("/auth/login", authHandler.Login)
	r.Post("/auth/logout", authHandler.Logout)

	r.Group(func(r chi.Router) {
		r.Use(middleware.Authenticate(authClient))
		// Financial routes added in service-specific implementation plans
	})

	port := viper.GetString("PORT")
	log.Printf("veloci-api listening on :%s", port)
	return http.ListenAndServe(":"+port, r)
}
