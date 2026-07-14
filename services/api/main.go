package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"strings"

	"github.com/danielgtaylor/huma/v2"
	"github.com/danielgtaylor/huma/v2/adapters/humachi"
	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/spf13/cobra"
	"github.com/spf13/viper"

	"github.com/veloci/api/authclient"
	"github.com/veloci/api/handler"
	"github.com/veloci/api/middleware"
	"github.com/veloci/api/queue"
	"github.com/veloci/api/store"
)

type appDBImpl struct {
	pool *pgxpool.Pool
}

func (d *appDBImpl) FindUserEntity(ctx context.Context, email string) (handler.UserEntity, error) {
	var ue handler.UserEntity
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
var migrateCmd = &cobra.Command{Use: "migrate", Short: "Seed RBAC roles and permissions", RunE: runMigrate}
var seedCmd = &cobra.Command{Use: "seed", Short: "Seed initial admin credential", RunE: runSeed}

func init() {
	rootCmd.AddCommand(serveCmd)
	rootCmd.AddCommand(migrateCmd)
	rootCmd.AddCommand(seedCmd)
	viper.SetEnvPrefix("VELOCI")
	viper.SetEnvKeyReplacer(strings.NewReplacer(".", "_"))
	viper.AutomaticEnv()
	viper.SetDefault("api.port", 8080)
}

func buildDBDSN() string {
	return fmt.Sprintf("postgres://%s:%s@%s:%d/%s?sslmode=disable",
		viper.GetString("database.app.user"),
		viper.GetString("database.app.password"),
		viper.GetString("database.host"),
		viper.GetInt("database.port"),
		viper.GetString("database.app.name"),
	)
}

func loadConfig() {
	configPath := os.Getenv("VELOCI_CONFIG_PATH")
	if configPath == "" {
		configPath = "config/veloci.toml"
	}
	viper.SetConfigFile(configPath)
	viper.SetConfigType("toml")
	if err := viper.ReadInConfig(); err != nil {
		log.Printf("config: %v — using defaults and env vars", err)
	}
}

func runServe(_ *cobra.Command, _ []string) error {
	loadConfig()

	authHost := viper.GetString("api.auth.host")
	authPort := viper.GetInt("api.auth.port")
	authURL := fmt.Sprintf("http://%s:%d", authHost, authPort)

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

	pool, err := pgxpool.New(context.Background(), buildDBDSN())
	if err != nil {
		return fmt.Errorf("database: %w", err)
	}
	defer pool.Close()

	authClient, err := authclient.NewClient(authURL)
	if err != nil {
		return fmt.Errorf("authclient: %w", err)
	}

	s := store.New(pool)

	perms, err := s.LoadPermissions(context.Background())
	if err != nil {
		log.Printf("warn: could not load permissions (empty cache): %v", err)
		perms = make(middleware.PermissionCache)
	}

	authHandler := handler.NewAuthHandler(authClient, &appDBImpl{pool: pool})

	r := chi.NewRouter()
	api := humachi.New(r, huma.DefaultConfig("Veloci API", "1.0.0"))

	handler.RegisterHealthRoutes(api)
	handler.RegisterAuthRoutes(api, authHandler)

	r.Group(func(r chi.Router) {
		r.Use(middleware.Authenticate(authClient))

		// Mount Huma-registered routes inside the authenticated group via a sub-router.
		subAPI := humachi.New(r, huma.DefaultConfig("Veloci API", "1.0.0"))

		handler.RegisterUsersRoutes(subAPI, s, authClient, pub, perms)
		handler.RegisterInstitutionsRoutes(subAPI, s, pub, perms)
		handler.RegisterAccountsRoutes(subAPI, s, pub, perms)
		handler.RegisterLabelsRoutes(subAPI, s, pub, perms)
		handler.RegisterEntriesRoutes(subAPI, s, pub, perms)
		handler.RegisterClassificationsRoutes(subAPI, s, pub, perms)
		handler.RegisterTransactionsRoutes(subAPI, s, pub, perms)
		handler.RegisterImportsRoutes(subAPI, s, pub, perms)
		handler.RegisterReviewRoutes(subAPI, s, pub, perms)
		handler.RegisterSnapshotsRoutes(subAPI, s, pub, perms)
		handler.RegisterProjectionsRoutes(subAPI, s, pub, perms)
		handler.RegisterAdminRoutes(subAPI, s, pub, perms)
		jobsHandler := handler.RegisterJobsRoutes(subAPI, s, pub, pool, perms)

		// Raw chi handlers that cannot use Huma (SSE, multipart upload).
		r.Post("/imports", handler.NewImportsHandler(s, pub).UploadImport)
		r.Get("/jobs/stream", jobsHandler.StreamJobs)
	})

	r.Get("/openapi.json", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		if err := json.NewEncoder(w).Encode(api.OpenAPI()); err != nil {
			http.Error(w, "failed to encode spec", http.StatusInternalServerError)
		}
	})

	port := viper.GetInt("api.port")
	log.Printf("veloci-api listening on :%d", port)
	return http.ListenAndServe(fmt.Sprintf(":%d", port), r)
}

func runMigrate(_ *cobra.Command, _ []string) error {
	loadConfig()

	pool, err := pgxpool.New(context.Background(), buildDBDSN())
	if err != nil {
		return fmt.Errorf("database: %w", err)
	}
	defer pool.Close()

	ctx := context.Background()

	// Seed roles.
	_, err = pool.Exec(ctx, `
		INSERT INTO roles (id, name)
		VALUES (gen_random_uuid(), 'entity_admin'), (gen_random_uuid(), 'entity_user')
		ON CONFLICT (name) DO NOTHING
	`)
	if err != nil {
		return fmt.Errorf("seed roles: %w", err)
	}

	// Seed permissions.
	perms := []string{
		"accounts:read", "accounts:write",
		"import:create",
		"entries:write",
		"classifications:write",
		"labels:write",
		"review:write",
		"reports:read",
		"users:manage",
		"entity:configure",
	}
	for _, p := range perms {
		_, err = pool.Exec(ctx, `
			INSERT INTO permissions (id, name)
			VALUES (gen_random_uuid(), $1)
			ON CONFLICT (name) DO NOTHING
		`, p)
		if err != nil {
			return fmt.Errorf("seed permission %s: %w", p, err)
		}
	}

	// entity_admin gets all permissions.
	adminPerms := perms

	// entity_user gets a subset.
	userPerms := []string{"accounts:read", "labels:write", "reports:read"}

	for _, rolePerms := range []struct {
		role  string
		perms []string
	}{
		{"entity_admin", adminPerms},
		{"entity_user", userPerms},
	} {
		for _, perm := range rolePerms.perms {
			_, err = pool.Exec(ctx, `
				INSERT INTO role_permissions (role_id, permission_id)
				SELECT r.id, p.id
				FROM roles r, permissions p
				WHERE r.name = $1 AND p.name = $2
				ON CONFLICT DO NOTHING
			`, rolePerms.role, perm)
			if err != nil {
				return fmt.Errorf("seed role_permission %s/%s: %w", rolePerms.role, perm, err)
			}
		}
	}

	log.Println("migrate: roles, permissions, and role_permissions seeded")
	return nil
}

func runSeed(_ *cobra.Command, _ []string) error {
	loadConfig()

	authHost := viper.GetString("api.auth.host")
	authPort := viper.GetInt("api.auth.port")
	authURL := fmt.Sprintf("http://%s:%d", authHost, authPort)

	email := viper.GetString("admin.email")
	password := viper.GetString("admin.password")
	if email == "" || password == "" {
		return fmt.Errorf("admin.email and admin.password must be set in config")
	}

	authClient, err := authclient.NewClient(authURL)
	if err != nil {
		return fmt.Errorf("authclient: %w", err)
	}

	ctx := context.Background()

	cred, err := authClient.CreateCredential(ctx, &authclient.CreateCredentialInputBody{
		Email:    email,
		Password: password,
	})
	if err != nil {
		log.Printf("seed: credential already exists or error: %v — continuing", err)
		return nil
	}

	pool, err := pgxpool.New(ctx, buildDBDSN())
	if err != nil {
		return fmt.Errorf("database: %w", err)
	}
	defer pool.Close()

	s := store.New(pool)
	userID, err := s.EnsureUser(ctx, email, cred.CredentialID)
	if err != nil {
		return fmt.Errorf("ensure user: %w", err)
	}

	log.Printf("seed: admin user ready (id=%s, credential_id=%s)", userID, cred.CredentialID)
	return nil
}
