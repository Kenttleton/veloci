package main

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/cenkalti/backoff/v4"
	"github.com/jackc/pgx/v5/pgxpool"
	echo "github.com/labstack/echo/v4"
	echomiddleware "github.com/labstack/echo/v4/middleware"
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

var rootCmd = &cobra.Command{Use: "veloci-web", Short: "Veloci API service"}

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
	viper.SetDefault("veloci.port", 8080)
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

func buildPool(ctx context.Context) (*pgxpool.Pool, error) {
	cfg, err := pgxpool.ParseConfig(buildDBDSN())
	if err != nil {
		return nil, err
	}
	if max := viper.GetInt32("veloci.pool.max"); max > 0 {
		cfg.MaxConns = max
	}

	var pool *pgxpool.Pool
	b := backoff.NewExponentialBackOff()
	b.MaxElapsedTime = 60 * time.Second
	attempt := 0
	err = backoff.Retry(func() error {
		attempt++
		p, dialErr := pgxpool.NewWithConfig(ctx, cfg)
		if dialErr != nil {
			log.Printf("database: connect attempt %d failed: %v", attempt, dialErr)
			return dialErr
		}
		if pingErr := p.Ping(ctx); pingErr != nil {
			p.Close()
			log.Printf("database: ping attempt %d failed: %v", attempt, pingErr)
			return pingErr
		}
		pool = p
		return nil
	}, backoff.WithContext(b, ctx))
	if err != nil {
		return nil, fmt.Errorf("database unavailable after 60s: %w", err)
	}
	return pool, nil
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

	pool, err := buildPool(context.Background())
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

	e := echo.New()
	e.HideBanner = true
	e.Use(echomiddleware.Logger())
	e.Use(echomiddleware.Recover())

	// Unknown routes: redirect to /budget
	e.RouteNotFound("/*", func(c echo.Context) error {
		return c.Redirect(http.StatusFound, "/budget")
	})

	// Static assets — no-store in dev so the browser re-fetches after air restarts
	if os.Getenv("VELOCI_DEV") == "true" {
		e.GET("/static/*", func(c echo.Context) error {
			c.Response().Header().Set("Cache-Control", "no-store")
			return c.File("static/" + c.Param("*"))
		})
	} else {
		e.Static("/static", "static")
	}

	// Register health (no auth)
	handler.RegisterHealthRoutes(e)

	// Public page routes
	e.GET("/login", pages.GetLogin)
	e.POST("/login", pages.PostLogin)

	// Session refresh (public — uses refresh cookie internally)
	e.POST("/api/session/refresh", pages.PostSessionRefresh)

	// Protected page routes (cookie auth → redirect on failure)
	pageGroup := e.Group("", middleware.AuthenticateCookieOrRedirect(authClient))
	pageGroup.POST("/logout", pages.PostLogout)
	pageGroup.GET("/", func(c echo.Context) error { return c.Redirect(http.StatusFound, "/budget") })
	pageGroup.GET("/budget", pages.Budget)
	pageGroup.GET("/reports", pages.Reports)
	pageGroup.GET("/ledger", pages.Ledger)
	pageGroup.GET("/activity", pages.Activity)
	pageGroup.GET("/accounts/:id", pages.Account)
	pageGroup.GET("/settings", pages.Settings)
	pageGroup.GET("/glossary", pages.Glossary)
	pageGroup.GET("/configuration", pages.Configuration)

	// SSE — cookie auth, same-origin EventSource sends cookies automatically
	e.GET("/api/jobs/stream", jobsHandler.StreamJobs, middleware.AuthenticateCookieOrRedirect(authClient))

	// API routes — Bearer or cookie auth
	api := e.Group("/api", middleware.AuthenticateBearerOrCookie(authClient))
	handler.RegisterUsersRoutes(api, s, authClient, perms)
	handler.RegisterInstitutionsRoutes(api, s, perms)
	handler.RegisterAccountsRoutes(api, s, perms)
	handler.RegisterLabelsRoutes(api, s, perms)
	handler.RegisterEntriesRoutes(api, s, pub, perms)
	handler.RegisterTransactionsRoutes(api, s, perms)
	handler.RegisterSnapshotsRoutes(api, s, perms)
	handler.RegisterProjectionsRoutes(api, s, perms)
	handler.RegisterAdminRoutes(api, s, perms)
	handler.RegisterJobsRoutes(api, jobsHandler, perms)
	handler.RegisterAutocompleteRoutes(api, s, perms)

	// Multipart upload — same API group auth already applied
	api.POST("/imports", handler.NewImportsHandler(s, pub).UploadImport)

	port := viper.GetInt("veloci.port")
	log.Printf("veloci listening on :%d", port)
	return e.Start(fmt.Sprintf(":%d", port))
}

func runMigrate(_ *cobra.Command, _ []string) error {
	loadConfig()

	pool, err := buildPool(context.Background())
	if err != nil {
		return fmt.Errorf("database: %w", err)
	}
	defer pool.Close()

	ctx := context.Background()

	// Seed Labels
	_, err = pool.Exec(ctx, `
		INSERT INTO labels (id, name)
		VALUES (gen_random_uuid(), 'Income'), (gen_random_uuid(), 'Spend')
		ON CONFLICT (name) DO NOTHING
	`)
	if err != nil {
		return fmt.Errorf("seed labels: %w", err)
	}

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
// concurrent startup ordering between veloci-auth and veloci-web.
func syncAdminUser(ctx context.Context, authClient *authclient.Client, s *store.Store, email, password string) error {
	var credentialID string
	b := backoff.NewExponentialBackOff()
	b.MaxElapsedTime = 60 * time.Second
	attempt := 0
	err := backoff.Retry(func() error {
		attempt++
		cred, err := authClient.ValidateCredential(ctx, &authclient.ValidateCredentialInputBody{
			Email:    email,
			Password: password,
		})
		if err != nil {
			log.Printf("admin sync: auth not ready (attempt %d): %v", attempt, err)
			return err
		}
		credentialID = cred.CredentialID
		return nil
	}, backoff.WithContext(b, ctx))
	if err != nil {
		return fmt.Errorf("could not validate admin credential after 60s: %w", err)
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

	pool, err := buildPool(context.Background())
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
