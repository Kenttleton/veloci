// specgen registers all veloci-api routes with Huma and writes the generated
// OpenAPI 3.1 spec to a file. Run from the services/api directory:
//
//	go run ./cmd/specgen [-o api/openapi.json]
package main

import (
	"encoding/json"
	"flag"
	"log"
	"os"

	"github.com/danielgtaylor/huma/v2"
	"github.com/danielgtaylor/huma/v2/adapters/humachi"
	"github.com/go-chi/chi/v5"

	"github.com/veloci/api/handler"
	"github.com/veloci/api/middleware"
)

func main() {
	out := flag.String("o", "api/openapi.json", "output file path")
	flag.Parse()

	r := chi.NewRouter()
	api := humachi.New(r, huma.Config{
		OpenAPI: &huma.OpenAPI{
			OpenAPI: "3.1.0",
			Info: &huma.Info{
				Title:   "Veloci API",
				Version: "1.0.0",
			},
		},
		DefaultFormat: "application/json",
	})

	// All routes registered on the same API — middleware is irrelevant for spec generation.
	perms := middleware.PermissionCache{}

	authHandler := handler.NewAuthHandler(nil, nil)

	handler.RegisterHealthRoutes(api)
	handler.RegisterAuthRoutes(api, authHandler)
	handler.RegisterLogoutRoute(api, authHandler)
	handler.RegisterUsersRoutes(api, nil, nil, nil, perms)
	handler.RegisterInstitutionsRoutes(api, nil, nil, perms)
	handler.RegisterAccountsRoutes(api, nil, nil, perms)
	handler.RegisterLabelsRoutes(api, nil, nil, perms)
	handler.RegisterEntriesRoutes(api, nil, nil, perms)
	handler.RegisterClassificationsRoutes(api, nil, nil, perms)
	handler.RegisterTransactionsRoutes(api, nil, nil, perms)
	handler.RegisterImportsRoutes(api, nil, nil, perms)
	handler.RegisterReviewRoutes(api, nil, nil, perms)
	handler.RegisterSnapshotsRoutes(api, nil, nil, perms)
	handler.RegisterProjectionsRoutes(api, nil, nil, perms)
	handler.RegisterAdminRoutes(api, nil, nil, perms)
	handler.RegisterJobsRoutes(api, handler.NewJobsHandler(nil, nil, nil), perms)

	spec := api.OpenAPI()
	b, err := json.MarshalIndent(spec, "", "  ")
	if err != nil {
		log.Fatalf("marshal spec: %v", err)
	}

	if err := os.MkdirAll("api", 0o755); err != nil {
		log.Fatalf("mkdir api: %v", err)
	}
	if err := os.WriteFile(*out, b, 0o644); err != nil {
		log.Fatalf("write %s: %v", *out, err)
	}
	log.Printf("wrote %s (%d bytes)", *out, len(b))
}
