#!/usr/bin/env sh
# Usage: toml-get.sh <dotted.section> <key> [config-file]
# Extracts a string or integer value from a TOML file.
# Works on busybox awk (Alpine) and gawk/mawk (Debian/macOS).
TOML="${3:-${VELOCI_CONFIG_PATH:-config/veloci.toml}}"
section="$1"
key="$2"

awk -v target="[$section]" -v k="$key" '
    $0 == target       { in_s=1; next }
    /^\[/              { in_s=0 }
    in_s && $1 == k    { val=$3; gsub(/^"|"$/, "", val); print val; exit }
' "$TOML"
