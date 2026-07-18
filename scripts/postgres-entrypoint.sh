#!/usr/bin/env sh
# Reads credentials from veloci.toml and exports them as postgres init vars,
# then hands off to the official postgres entrypoint.
set -e

TOML="${VELOCI_CONFIG_PATH:-/etc/veloci/veloci.toml}"

toml_get() {
    awk -v target="[$1]" -v k="$2" '
        $0 == target       { in_s=1; next }
        /^\[/              { in_s=0 }
        in_s && $1 == k    { val=$3; gsub(/^"|"$/, "", val); print val; exit }
    ' "$TOML"
}

export POSTGRES_USER=$(toml_get "database.superuser" "user")
export POSTGRES_PASSWORD=$(toml_get "database.superuser" "password")
export VELOCI_AUTH_DB=$(toml_get "database.auth" "name")
export VELOCI_AUTH_DB_USER=$(toml_get "database.auth" "user")
export VELOCI_AUTH_DB_PASSWORD=$(toml_get "database.auth" "password")
export VELOCI_APP_DB=$(toml_get "database.app" "name")
export VELOCI_APP_DB_USER=$(toml_get "database.app" "user")
export VELOCI_APP_DB_PASSWORD=$(toml_get "database.app" "password")

exec docker-entrypoint.sh "$@"
