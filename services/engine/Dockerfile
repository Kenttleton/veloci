# Build stage — use rust:1-alpine (latest stable) since rust:1.95-alpine is not
# yet published on Docker Hub as of this writing.  Pin to 1.95 or later once
# that tag is available.
FROM rust:1-alpine AS build
RUN apk add --no-cache musl-dev
WORKDIR /app
COPY Cargo.toml Cargo.lock ./
# Warm the dependency cache with a stub binary so the real COPY only rebuilds
# application code.
RUN mkdir src && printf 'fn main() {}' > src/main.rs \
    && cargo build --release \
    && rm -r src
COPY src ./src
# Touch main.rs to force cargo to re-link the final binary.
RUN touch src/main.rs && cargo build --release

FROM alpine:3.24
COPY --from=build /app/target/release/veloci-engine /veloci-engine
ENTRYPOINT ["/veloci-engine"]
