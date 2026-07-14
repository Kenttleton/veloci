package handler

import (
	"context"
	"net/http"

	"github.com/danielgtaylor/huma/v2"
)

type healthOutput struct {
	Body struct {
		Status string `json:"status" doc:"Always 'ok' when the service is up"`
	}
}

func health(ctx context.Context, _ *struct{}) (*healthOutput, error) {
	out := &healthOutput{}
	out.Body.Status = "ok"
	return out, nil
}

// RegisterHealthRoutes registers the health check endpoint.
func RegisterHealthRoutes(api huma.API) {
	huma.Register(api, huma.Operation{
		OperationID: "health",
		Method:      http.MethodGet,
		Path:        "/health",
		Summary:     "Service health check",
		Tags:        []string{"system"},
	}, health)
}
