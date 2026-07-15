FROM golang:1.26-alpine AS build
WORKDIR /app
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN go build -o veloci-api .

FROM alpine:3.24
COPY --from=build /app/veloci-api /veloci-api
ENTRYPOINT ["/veloci-api", "serve"]
