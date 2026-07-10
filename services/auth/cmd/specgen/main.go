// specgen registers all veloci-auth routes with Huma and writes the generated
// OpenAPI 3.1 spec to a file. Run from the services/auth directory:
//
//	go run ./cmd/specgen [-o api/openapi.json]
//
//go:generate go run ./cmd/specgen -o api/openapi.json
package main

import (
	"encoding/json"
	"flag"
	"log"
	"os"

	"github.com/danielgtaylor/huma/v2"
	"github.com/danielgtaylor/huma/v2/adapters/humachi"
	"github.com/go-chi/chi/v5"
	"github.com/veloci/auth/internal/handlers"
)

func main() {
	out := flag.String("o", "api/openapi.json", "output file path")
	flag.Parse()

	// Handlers need no real DB or signing key — routes register purely from types.
	placeholder := make([]byte, 32)
	creds := handlers.NewCredentials(nil)
	toks := handlers.NewTokens(nil, placeholder, handlers.DefaultTokenConfig())
	inv := handlers.NewInvite(nil, handlers.DefaultInviteConfig())

	r := chi.NewRouter()
	api := humachi.New(r, huma.Config{
		OpenAPI: &huma.OpenAPI{
			OpenAPI: "3.1.0",
			Info: &huma.Info{
				Title:   "Veloci Auth",
				Version: "1.0.0",
			},
		},
		DefaultFormat: "application/json",
	})

	handlers.RegisterCredentialRoutes(api, creds)
	handlers.RegisterTokenRoutes(api, toks)
	handlers.RegisterInviteRoutes(api, inv)

	spec := api.OpenAPI()
	b, err := json.MarshalIndent(spec, "", "  ")
	if err != nil {
		log.Fatalf("marshal spec: %v", err)
	}

	if err := os.WriteFile(*out, b, 0o644); err != nil {
		log.Fatalf("write %s: %v", *out, err)
	}
	log.Printf("wrote %s (%d bytes)", *out, len(b))
}
