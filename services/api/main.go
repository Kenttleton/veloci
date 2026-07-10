package main

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"os"
	"strings"

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
	viper.SetEnvPrefix("VELOCI")
	viper.SetEnvKeyReplacer(strings.NewReplacer(".", "_"))
	viper.AutomaticEnv()
	viper.SetDefault("api.port", 8080)
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

	authHost := viper.GetString("api.auth.host")
	authPort := viper.GetInt("api.auth.port")
	authURL := fmt.Sprintf("http://%s:%d", authHost, authPort)

	dbDSN := fmt.Sprintf("postgres://%s:%s@%s:%d/%s?sslmode=disable",
		viper.GetString("database.app.user"),
		viper.GetString("database.app.password"),
		viper.GetString("database.host"),
		viper.GetInt("database.port"),
		viper.GetString("database.app.name"),
	)

	amqpURI := fmt.Sprintf("amqp://%s:%s@%s:%d/",
		viper.GetString("rabbitmq.user"),
		viper.GetString("rabbitmq.password"),
		viper.GetString("rabbitmq.host"),
		viper.GetInt("rabbitmq.port"),
	)

	pub, err := queue.NewPublisher(amqpURI)
	if err != nil {
		return fmt.Errorf("queue: %w", err)
	}
	_ = pub // TODO: pass to financial route handlers in service implementation plan

	pool, err := pgxpool.New(context.Background(), dbDSN)
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

	port := viper.GetInt("api.port")
	log.Printf("veloci-api listening on :%d", port)
	return http.ListenAndServe(fmt.Sprintf(":%d", port), r)
}
