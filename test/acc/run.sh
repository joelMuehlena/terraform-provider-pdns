#!/usr/bin/env bash
# Runs the acceptance tests against a PowerDNS Authoritative server started
# with docker compose, then tears it down.
#
#   PDNS_VERSION=50 PDNS_BACKEND=gpgsql test/acc/run.sh [go test flags...]
#
# Environment:
#   PDNS_VERSION   pdns-auth series: 49, 50 or 51 (also 4.9, 5.0, 5.1; default 51)
#   PDNS_IMAGE     pdns-auth image, default from images/pdns-auth-<series>.Dockerfile
#   POSTGRES_IMAGE postgres image (gpgsql), default from images/postgres.Dockerfile
#   PDNS_BACKEND   gsqlite3 or gpgsql (default gsqlite3), see compose.<backend>.yml
#   COMPOSE        compose command (default "docker compose"), e.g. "podman compose"
#   KEEP=1         leave the server running afterwards
#   PDNS_ACC_PROJECT  compose project name (default pdns-acc-<version>-<backend>),
#                  set it to run several servers of one combination at once
#   PDNS_ACC_PORT, PDNS_ACC_DNS_PORT, PDNS_ACC_API_KEY, PDNS_ACC_SERVER_ARGS:
#                  see compose.yml, e.g. PDNS_ACC_SERVER_ARGS=--dnsupdate-require-tsig=yes
#                  (PowerDNS 5.0+) to run the tests against the recommended setting
#
# A terraform binary must be on PATH. For OpenTofu, set
# TF_ACC_TERRAFORM_PATH to the tofu binary and
# TF_ACC_PROVIDER_HOST=registry.opentofu.org.
#
# The images are pinned to exact versions in images/*.Dockerfile, which
# Dependabot keeps up to date (they are never built). To bump one by hand,
# edit its FROM line.
set -euo pipefail

dir=$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)
root=$(cd "$dir/../.." && pwd)

export PDNS_VERSION=${PDNS_VERSION:-51}
PDNS_VERSION=${PDNS_VERSION//./}
backend=${PDNS_BACKEND:-gsqlite3}
if [[ ! -f "$dir/compose.$backend.yml" ]]; then
  echo "unknown PDNS_BACKEND '$backend' (no $dir/compose.$backend.yml)" >&2
  exit 2
fi

# image_of prints the image of the FROM line of images/<name>.Dockerfile.
image_of() {
  local file=$dir/images/$1.Dockerfile image
  if [[ ! -f "$file" ]]; then
    echo "no image pinned for '$1' (no $file)" >&2
    exit 2
  fi
  image=$(sed -n 's/^FROM[[:space:]]*//p' "$file")
  if [[ -z "$image" ]]; then
    echo "no FROM line in $file" >&2
    exit 2
  fi
  echo "$image"
}
# Plain assignments, so that set -e sees a failing image_of.
PDNS_IMAGE=${PDNS_IMAGE:-$(image_of "pdns-auth-$PDNS_VERSION")}
POSTGRES_IMAGE=${POSTGRES_IMAGE:-$(image_of postgres)}
export PDNS_IMAGE POSTGRES_IMAGE
echo "pdns image: $PDNS_IMAGE" >&2

export PDNS_ACC_API_KEY=${PDNS_ACC_API_KEY:-acc-test-key}
export PDNS_ACC_PORT=${PDNS_ACC_PORT:-8081}
export PDNS_ACC_DNS_PORT=${PDNS_ACC_DNS_PORT:-15353}
export PDNS_ACC_SERVER_ARGS=${PDNS_ACC_SERVER_ARGS:-}
project=${PDNS_ACC_PROJECT:-pdns-acc-$PDNS_VERSION-$backend}

read -r -a compose <<<"${COMPOSE:-docker compose}"
compose+=(-p "$project" -f "$dir/compose.yml" -f "$dir/compose.$backend.yml")

down() {
  if [[ "${KEEP:-}" != 1 ]]; then
    "${compose[@]}" down --volumes --remove-orphans
  fi
}
trap down EXIT

if ! "${compose[@]}" up --detach --wait; then
  "${compose[@]}" logs
  exit 1
fi

cd "$root"
TF_ACC=1 \
  PDNS_ACC_ENDPOINT="http://127.0.0.1:$PDNS_ACC_PORT" \
  PDNS_ACC_DNS_ADDR="127.0.0.1:$PDNS_ACC_DNS_PORT" \
  go test -count=1 -run '^TestAcc' "$@" ./internal/provider/
