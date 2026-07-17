package main

//go:generate ogen --target ./authclient --package authclient --clean ../auth/api/openapi.json
//go:generate go run ./cmd/patchclient
