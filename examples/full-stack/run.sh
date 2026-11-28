#!/usr/bin/env bash
# Smoke test of the full-stack example: starts the containers, applies both
# stacks, checks the DNS answers of PowerDNS, the Recursor and BIND, then
# destroys everything again. Runs with the bash 3.2 of macOS.
#
#   ./run.sh
#   TF=tofu COMPOSE="podman compose" ./run.sh
#
# Environment:
#   TF          terraform or tofu (default terraform)
#   COMPOSE     compose command (default "docker compose")
#   PROJECT     compose project name (default pdns-full-stack)
#   KEEP=1      leave the containers and the stacks in place
#   BIND_IMAGE  see compose.yml (BIND on arm64 hosts without Rosetta)
#   PDNS_API_PORT, AUTH_DNS_PORT, RECURSOR_DNS_PORT, BIND_DNS_PORT,
#   PDNS_API_KEY: see compose.yml
#
# Needs dig and nsupdate (bind-tools / dnsutils). To test a locally built
# provider, point TF_CLI_CONFIG_FILE at a CLI configuration with
# dev_overrides (see README.md).
set -euo pipefail

dir=$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)
cd "$dir"

TF=${TF:-terraform}
project=${PROJECT:-pdns-full-stack}
read -r -a compose <<<"${COMPOSE:-docker compose}"
container_cli=${compose[0]}
compose+=(-p "$project" -f "$dir/compose.yml")

export PDNS_API_PORT=${PDNS_API_PORT:-18281}
export AUTH_DNS_PORT=${AUTH_DNS_PORT:-15480}
export RECURSOR_DNS_PORT=${RECURSOR_DNS_PORT:-15481}
export BIND_DNS_PORT=${BIND_DNS_PORT:-15482}
export PDNS_API_KEY=${PDNS_API_KEY:-example-api-key}
export TF_VAR_pdns_endpoint=http://127.0.0.1:$PDNS_API_PORT
export TF_VAR_pdns_api_key=$PDNS_API_KEY
export TF_VAR_dns_port=$AUTH_DNS_PORT
export TF_IN_AUTOMATION=1 TF_INPUT=0

for tool in "$TF" dig nsupdate; do
  if ! command -v "$tool" >/dev/null; then
    echo "$tool is not installed" >&2
    exit 2
  fi
done
for stack in infra dynamic; do
  if [[ -e "$stack/terraform.tfstate" ]]; then
    echo "$stack/terraform.tfstate exists: destroy that stack or remove the state first" >&2
    exit 2
  fi
done

step() { printf '\n==> %s\n' "$*"; }
ok() { printf 'ok: %s\n' "$*"; }
fail() {
  printf 'FAIL: %s\n' "$*" >&2
  exit 1
}

# dig_short <port> <name> <type> [dig options...]
dig_short() {
  local port=$1 name=$2 type=$3
  shift 3
  dig @127.0.0.1 -p "$port" +short +tries=1 +time=2 "$@" "$name" "$type" 2>/dev/null || true
}

serial() { dig_short "$1" example.test SOA | awk '{ print $3 }'; }

# expect <description> <pattern> <command...>: retries the command for up to
# 60 seconds until its output matches the (glob) pattern; transfers and
# NOTIFY take a few seconds.
expect() {
  local description=$1 expected=$2 got=""
  shift 2
  local deadline=$((SECONDS + 60))
  while ((SECONDS < deadline)); do
    got=$("$@")
    # shellcheck disable=SC2053 # $expected is a pattern
    if [[ "$got" == $expected ]]; then
      ok "$description: $(echo "$got" | tr '\n' ' ')"
      return 0
    fi
    sleep 2
  done
  fail "$description: expected '$expected', got '$got'"
}

sorted() { "$@" | sort; }
same_serial() {
  local pdns bind
  pdns=$(serial "$AUTH_DNS_PORT")
  bind=$(serial "$BIND_DNS_PORT")
  if [[ -n "$pdns" && "$pdns" == "$bind" ]]; then echo "same serial $pdns"; else echo "pdns=$pdns bind=$bind"; fi
}

tf() { "$TF" -chdir="$dir/$1" "${@:2}"; }

remove_state() {
  rm -rf infra/.terraform infra/.terraform.lock.hcl infra/terraform.tfstate infra/terraform.tfstate.backup \
    dynamic/.terraform dynamic/.terraform.lock.hcl dynamic/terraform.tfstate dynamic/terraform.tfstate.backup
}

# On failure: show the logs, destroy what was created, remove the containers.
teardown() {
  local status=$?
  ((status == 0)) && return
  set +e
  "${compose[@]}" ps -a
  "${compose[@]}" logs --tail 50
  if [[ "${KEEP:-}" == 1 ]]; then
    echo "KEEP=1: leaving the containers and stacks in place" >&2
    return
  fi
  step "destroy and clean up after the failure"
  [[ -e dynamic/terraform.tfstate ]] && tf dynamic destroy -auto-approve -no-color
  [[ -e infra/terraform.tfstate ]] && tf infra destroy -auto-approve -no-color
  "${compose[@]}" down --volumes --remove-orphans
  remove_state
}
trap teardown EXIT

step "start postgres, pdns-auth and the recursor"
"${compose[@]}" up --detach --wait pdns-auth recursor

step "apply infra"
tf infra init -no-color
tf infra apply -auto-approve -no-color
[[ -s bind/keys/tsig-keys.conf ]] || fail "infra did not write bind/keys/tsig-keys.conf"

step "start BIND with the TSIG keys of infra"
"${compose[@]}" up --detach --wait bind

step "check infra"
expect "recursor resolves www.example.test from pdns" 192.0.2.10 \
  dig_short "$RECURSOR_DNS_PORT" www.example.test A
expect "BIND serves example.test (secondary, AXFR with bind-axfr)" 192.0.2.10 \
  dig_short "$BIND_DNS_PORT" www.example.test A
expect "BIND has the SOA serial of pdns" "same serial *" same_serial
expect "pdns serves legacy.test (Slave zone, AXFR from BIND with pdns-secondary)" \
  '"served by BIND, transferred by PowerDNS"' dig_short "$AUTH_DNS_PORT" legacy.test TXT
expect "recursor resolves www.legacy.test" 192.0.2.20 \
  dig_short "$RECURSOR_DNS_PORT" www.legacy.test A

axfr_key=$(tf infra output -raw bind_axfr_key)
records=$(dig @127.0.0.1 -p "$AUTH_DNS_PORT" -y "$axfr_key" +noall +answer example.test AXFR | grep -c . || true)
((records > 0)) || fail "signed AXFR of example.test from pdns returned no records"
ok "signed AXFR of example.test from pdns: $records records"
if dig @127.0.0.1 -p "$AUTH_DNS_PORT" +noall +answer example.test AXFR | grep -q SOA; then
  fail "unsigned AXFR of example.test was allowed"
fi
ok "unsigned AXFR of example.test is refused"

# Unsigned update: refused, as the server runs with dnsupdate-require-tsig.
out=$(printf 'server 127.0.0.1 %s\nzone example.test\nupdate add unsigned.example.test 60 A 192.0.2.66\nsend\n' \
  "$AUTH_DNS_PORT" | nsupdate 2>&1 || true)
[[ "$out" == *REFUSED* ]] || fail "unsigned update was not refused: $out"
[[ -z "$(dig_short "$AUTH_DNS_PORT" unsigned.example.test A)" ]] || fail "unsigned update was applied"
ok "unsigned update is refused ($out)"

step "apply dynamic (RFC2136 updates signed with the dynamic key)"
tf dynamic init -no-color
tf dynamic apply -auto-approve -no-color

step "check dynamic"
expect "recursor resolves app.example.test" "$(printf '192.0.2.100\n192.0.2.101')" \
  sorted dig_short "$RECURSOR_DNS_PORT" app.example.test A
expect "recursor resolves the TXT record of app.example.test" '"managed by the dynamic stack"' \
  dig_short "$RECURSOR_DNS_PORT" app.example.test TXT
expect "BIND got the update (NOTIFY and AXFR)" "$(printf '192.0.2.100\n192.0.2.101')" \
  sorted dig_short "$BIND_DNS_PORT" app.example.test A
expect "BIND has the SOA serial of pdns" "same serial *" same_serial

step "plans are empty"
for stack in infra dynamic; do
  rc=0
  tf "$stack" plan -detailed-exitcode -no-color >/dev/null || rc=$?
  ((rc == 0)) || fail "plan of $stack is not empty (exit code $rc)"
  ok "no changes in $stack"
done

if [[ "${KEEP:-}" == 1 ]]; then
  echo "KEEP=1: leaving the containers and stacks in place"
  exit 0
fi

step "destroy dynamic and infra"
tf dynamic destroy -auto-approve -no-color
expect "app.example.test is gone from pdns" "" dig_short "$AUTH_DNS_PORT" app.example.test A
tf infra destroy -auto-approve -no-color
expect "pdns no longer serves example.test" REFUSED \
  sh -c "dig @127.0.0.1 -p $AUTH_DNS_PORT +tries=1 +time=2 example.test SOA | sed -n 's/.*status: \([A-Z]*\),.*/\1/p'"
[[ ! -e bind/keys/tsig-keys.conf ]] || fail "infra destroy left bind/keys/tsig-keys.conf"
for stack in infra dynamic; do
  [[ -z "$(tf "$stack" state list)" ]] || fail "$stack still has resources in state"
done
ok "both stacks are destroyed"

step "remove the containers"
"${compose[@]}" down --volumes --remove-orphans
[[ -z "$("${compose[@]}" ps -a -q)" ]] || fail "containers of $project are left"
[[ -z "$("$container_cli" volume ls -q --filter "label=com.docker.compose.project=$project")" ]] ||
  fail "volumes of $project are left"
remove_state
ok "containers and volumes removed"
printf '\nfull-stack example: all checks passed\n'
