package main

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/danielgtaylor/huma/v2"
	"github.com/danielgtaylor/huma/v2/adapters/humachi"
	"github.com/go-chi/chi/v5"
	chimiddleware "github.com/go-chi/chi/v5/middleware"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/spf13/cobra"
	"github.com/spf13/viper"

	"github.com/veloci/veloci/authclient"
	"github.com/veloci/veloci/handler"
	"github.com/veloci/veloci/middleware"
	"github.com/veloci/veloci/page"
	"github.com/veloci/veloci/queue"
	"github.com/veloci/veloci/store"
)


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

	authHost := viper.GetString("auth.host")
	authPort := viper.GetInt("auth.port")
	authURL := fmt.Sprintf("http://%s:%d", authHost, authPort)

	amqpURI := fmt.Sprintf("amqp://%s:%s@%s:%d/",
		viper.GetString("rabbitmq.user"),
		viper.GetString("rabbitmq.password"),
		viper.GetString("rabbitmq.host"),
		viper.GetInt("rabbitmq.port"),
	)

	pub := queue.NewPublisher(amqpURI)

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

	adminEmail := viper.GetString("auth.admin.email")
	adminPass := viper.GetString("auth.admin.password")
	if adminEmail != "" && adminPass != "" {
		if err := syncAdminUser(context.Background(), authClient, s, adminEmail, adminPass); err != nil {
			log.Printf("warn: admin user sync failed: %v", err)
		}
	}

	perms, err := s.LoadPermissions(context.Background())
	if err != nil {
		log.Printf("warn: could not load permissions (empty cache): %v", err)
		perms = make(middleware.PermissionCache)
	}

	pages := page.NewServer(s, authClient, pool)
	jobsHandler := handler.NewJobsHandler(s, pub, pool)

	r := chi.NewRouter()
	r.Use(chimiddleware.Logger)

	// Static assets (CSS, JS islands).
	r.Handle("/static/*", http.StripPrefix("/static/", http.FileServer(http.Dir("static"))))

	// ─── Public page routes ───────────────────────────────────────────────────
	r.Get("/login", pages.GetLogin)
	r.Post("/login", pages.PostLogin)

	// ─── Protected page routes (cookie auth) ─────────────────────────────────
	r.Group(func(r chi.Router) {
		r.Use(middleware.AuthenticateCookieOrRedirect(authClient))
		r.Post("/logout", pages.PostLogout)
		r.Get("/", func(w http.ResponseWriter, r *http.Request) {
			http.Redirect(w, r, "/budget", http.StatusFound)
		})
		r.Get("/budget", pages.Budget)
		r.Get("/reports", pages.Reports)
		r.Get("/ledger", pages.Ledger)
		r.Get("/activity", pages.Activity)
		r.Get("/accounts/{id}", pages.Account)
		r.Get("/settings", pages.Settings)
		r.Get("/glossary", pages.Glossary)
		r.Get("/configuration", pages.Configuration)
	})

	// ─── Internal API routes (cookie or Bearer, same-origin JS islands) ───────
	internalAPI := humachi.New(r, huma.DefaultConfig("Veloci", "1.0.0"))
	handler.RegisterHealthRoutes(internalAPI)

	r.Group(func(r chi.Router) {
		r.Use(middleware.AuthenticateBearerOrCookie(authClient))
		subAPI := humachi.New(r, huma.DefaultConfig("Veloci", "1.0.0"))

		handler.RegisterUsersRoutes(subAPI, s, authClient, pub, perms)
		handler.RegisterInstitutionsRoutes(subAPI, s, pub, perms)
		handler.RegisterAccountsRoutes(subAPI, s, pub, perms)
		handler.RegisterLabelsRoutes(subAPI, s, pub, perms)
		handler.RegisterEntriesRoutes(subAPI, s, pub, perms)
		handler.RegisterClassificationsRoutes(subAPI, s, pub, perms)
		handler.RegisterTransactionsRoutes(subAPI, s, pub, perms)
		handler.RegisterSnapshotsRoutes(subAPI, s, pub, perms)
		handler.RegisterProjectionsRoutes(subAPI, s, pub, perms)
		handler.RegisterAdminRoutes(subAPI, s, pub, perms)
		handler.RegisterJobsRoutes(subAPI, jobsHandler, perms)

		// Raw chi handler (multipart upload cannot use Huma).
		r.Post("/imports", handler.NewImportsHandler(s, pub).UploadImport)
	})

	// SSE uses cookie auth (same-origin; EventSource sends cookies automatically).
	r.With(middleware.AuthenticateCookieOrRedirect(authClient)).Get("/jobs/stream", jobsHandler.StreamJobs)

	_ = internalAPI // suppress unused warning; routes registered via side-effect

	port := viper.GetInt("api.port")
	log.Printf("veloci listening on :%d", port)
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

// syncAdminUser ensures the admin user exists in veloci_app and belongs to at least
// one entity. It retries the auth credential lookup for up to 30 seconds to tolerate
// concurrent startup ordering between veloci-auth and veloci-api.
func syncAdminUser(ctx context.Context, authClient *authclient.Client, s *store.Store, email, password string) error {
	var credentialID string
	for i := range 30 {
		cred, err := authClient.ValidateCredential(ctx, &authclient.ValidateCredentialInputBody{
			Email:    email,
			Password: password,
		})
		if err == nil {
			credentialID = cred.CredentialID
			break
		}
		if i < 29 {
			log.Printf("admin sync: auth not ready (attempt %d/30): %v", i+1, err)
			time.Sleep(time.Second)
		}
	}
	if credentialID == "" {
		return fmt.Errorf("could not validate admin credential after 30 attempts")
	}

	userID, err := s.EnsureUser(ctx, email, "Server Admin", credentialID)
	if err != nil {
		return fmt.Errorf("ensure user: %w", err)
	}

	entityID, err := s.EnsureAdminEntity(ctx, "Home")
	if err != nil {
		return fmt.Errorf("ensure entity: %w", err)
	}

	if err := s.EnsureEntityUser(ctx, userID, entityID, "entity_admin"); err != nil {
		return fmt.Errorf("ensure entity membership: %w", err)
	}

	log.Printf("sync: admin user ready (id=%s, entity_id=%s)", userID, entityID)
	return nil
}


func runSeed(_ *cobra.Command, _ []string) error {
	loadConfig()

	authHost := viper.GetString("auth.host")
	authPort := viper.GetInt("auth.port")
	authURL := fmt.Sprintf("http://%s:%d", authHost, authPort)

	email := viper.GetString("auth.admin.email")
	password := viper.GetString("auth.admin.password")
	if email == "" || password == "" {
		return fmt.Errorf("admin.email and admin.password must be set in config")
	}

	authClient, err := authclient.NewClient(authURL)
	if err != nil {
		return fmt.Errorf("authclient: %w", err)
	}

	pool, err := pgxpool.New(context.Background(), buildDBDSN())
	if err != nil {
		return fmt.Errorf("database: %w", err)
	}
	defer pool.Close()

	s := store.New(pool)
	if err := syncAdminUser(context.Background(), authClient, s, email, password); err != nil {
		return fmt.Errorf("seed: %w", err)
	}
	return nil
}
