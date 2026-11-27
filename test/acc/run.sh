#!/usr/bin/env bash
# Runs the acceptance tests against a PowerDNS Authoritative server started
# with docker compose, then tears it down.
#
#   PDNS_VERSION=50 PDNS_BACKEND=gpgsql test/acc/run.sh [go test flags...]
#
# Environment:
#   PDNS_VERSION   pdns-auth series: 49, 50 or 51 (also 4.9, 5.0, 5.1; default 51)
#   PDNS_BACKEND   gsqlite3 or gpgsql (default gsqlite3), see compose.<backend>.yml
#   COMPOSE        compose command (default "docker compose"), e.g. "podman compose"
#   KEEP=1         leave the server running afterwards
#   PDNS_ACC_PROJECT  compose project name (default pdns-acc-<version>-<backend>),
#                  set it to run several servers of one combination at once
#   PDNS_ACC_PORT, PDNS_ACC_DNS_PORT, PDNS_ACC_API_KEY, PDNS_ACC_SERVER_ARGS:
#                  see compose.yml, e.g. PDNS_ACC_SERVER_ARGS=--dnsupdate-require-tsig=yes
#                  (PowerDNS 5.0+) to run the tests against the recommended setting
#
# A terraform (or tofu, via TF_ACC_TERRAFORM_PATH) binary must be on PATH.
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
