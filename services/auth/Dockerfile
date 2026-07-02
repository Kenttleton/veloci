FROM golang:1.26-alpine AS build
WORKDIR /app
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN go build -o auth .

FROM alpine:3.24
COPY --from=build /app/auth /auth
ENTRYPOINT ["/auth", "serve"]
