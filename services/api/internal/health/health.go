package health

import (
	"context"
	"net/http"

	"github.com/danielgtaylor/huma/v2"
)

type HealthOutput struct {
	Body struct {
		Status string `json:"status" doc:"Always 'ok' when the service is up"`
	}
}

// Health returns the service liveness status.
func Health(ctx context.Context, _ *struct{}) (*HealthOutput, error) {
	out := &HealthOutput{}
	out.Body.Status = "ok"
	return out, nil
}

// RegisterRoutes registers the health check endpoint.
func RegisterRoutes(api huma.API) {
	huma.Register(api, huma.Operation{
		OperationID: "health",
		Method:      http.MethodGet,
		Path:        "/health",
		Summary:     "Service health check",
		Tags:        []string{"system"},
	}, Health)
}
