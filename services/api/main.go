package main

import (
	"fmt"
	"log"
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/spf13/cobra"
	"github.com/spf13/viper"

	"github.com/veloci/api/internal/authclient"
	"github.com/veloci/api/internal/handlers"
	"github.com/veloci/api/internal/middleware"
	"github.com/veloci/api/internal/queue"
)

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

	if _, err := queue.NewPublisher(viper.GetString("RABBITMQ_URL")); err != nil {
		return fmt.Errorf("queue: %w", err)
	}

	authClient := authclient.New(authURL)

	// TODO: wire real appDB in service implementation plan
	authHandler := handlers.NewAuth(authURL, nil)

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
