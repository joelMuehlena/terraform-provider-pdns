# Full-stack example

A local DNS setup in containers, managed with Terraform or OpenTofu in two
stacks with separate state:

- **PowerDNS Authoritative Server 5.1** with PostgreSQL (`gpgsql`), the HTTP
  API and RFC2136 dynamic updates that must be signed with TSIG
  (`dnsupdate-require-tsig=yes`).
- **PowerDNS Recursor 5.4**, which forwards the example zones to it.
- **BIND 9.20** as secondary of a PowerDNS zone (AXFR signed with TSIG, NOTIFY
  from PowerDNS) and as primary of a zone that PowerDNS transfers as a
  secondary (`Slave`) zone.
- **`infra/`** manages the zones, records, TSIG keys and their attachments
  with this provider.
- **`dynamic/`** adds records with the `hashicorp/dns` provider through
  signed RFC2136 updates, using the TSIG key from the outputs of `infra/`.

```text
                    host (127.0.0.1)
  dig ───────────────┐    terraform/tofu       terraform/tofu
                     │    infra/               dynamic/ (hashicorp/dns)
                     │      │ HTTP API :18281    │ RFC2136, TSIG "dynamic"
                     ▼      ▼                    ▼ :15480
 ┌─ compose network 10.53.0.0/24 ──────────────────────────────────────────┐
 │                                                                         │
 │  recursor 10.53.0.20 ──forward──▶ pdns-auth 10.53.0.10 ───▶ postgres    │
 │  :15481                           :15480 DNS, :18281 API    10.53.0.5   │
 │                                     │  ▲                                │
 │       example.test (Master):        │  │  legacy.test (Slave):          │
 │       NOTIFY, AXFR with "bind-axfr" │  │  NOTIFY, AXFR with             │
 │                                     ▼  │  "pdns-secondary"              │
 │                                   bind 10.53.0.30                       │
 │                                   :15482                                │
 │                                   secondary of example.test,            │
 │                                   primary of legacy.test                │
 └─────────────────────────────────────────────────────────────────────────┘
```

| Zone            | Primary                         | Secondary                        | TSIG key (attachment)                  |
| --------------- | ------------------------------- | -------------------------------- | -------------------------------------- |
| `example.test.` | PowerDNS (`kind = "Master"`)    | BIND                             | `bind-axfr` (`axfr = true`)            |
|                 |                                 |                                  | `dynamic` (`dnsupdate = true`)         |
| `legacy.test.`  | BIND (`bind/zones/`)            | PowerDNS (`kind = "Slave"`)      | `pdns-secondary` (`secondary = true`)  |

All ports are published on 127.0.0.1 only: API 18281, PowerDNS 15480, Recursor
15481, BIND 15482 (see `compose.yml` to change them). Inside the network every
DNS server listens on port 5353.

## Prerequisites

- Docker with Compose v2, or podman with `podman compose`.
- Terraform >= 1.6 or OpenTofu >= 1.6.
- `dig` and `nsupdate` (`bind-tools`, `dnsutils` or `bind9-dnsutils`) for the
  checks.
- **Provider version:** the example needs `pdns_tsig_key`,
  `pdns_zone_tsig_key` and the `ttl` and `addresses` of `pdns_zone`, which are
  not in v0.3.0, so it requires `>= 0.4.0`. Until that version is released,
  use a local build, see [Using a local build of the
  provider](#using-a-local-build-of-the-provider).
- **arm64 hosts** (Apple silicon, Linux arm64): the ISC BIND image is only
  built for amd64. Docker Desktop runs it with Rosetta; where amd64 images run
  under qemu (podman machine without Rosetta, Linux arm64), BIND crashes. Then
  set `export BIND_IMAGE=localhost/pdns-full-stack-bind`: that image cannot
  be pulled, so compose builds it from `bind/Dockerfile` (BIND from Alpine).

## Run it

`./run.sh` runs all the steps below and checks the answers (see
[Smoke test](#smoke-test)). By hand, from this directory (use `tofu` instead
of `terraform` and `podman compose` instead of `docker compose` as you like):

1. Start PostgreSQL, PowerDNS and the Recursor:

   ```sh
   docker compose up -d --wait pdns-auth recursor
   ```

2. Apply the infra stack. It creates the zones, the records, the three TSIG
   keys with their attachments, and writes the keys of BIND to
   `bind/keys/tsig-keys.conf`:

   ```sh
   terraform -chdir=infra init
   terraform -chdir=infra apply
   ```

3. Start BIND. It needs the key file, so it is started after step 2. It
   transfers `example.test` from PowerDNS, and its NOTIFY of `legacy.test`
   makes PowerDNS transfer that zone:

   ```sh
   docker compose up -d --wait bind
   ```

4. Apply the dynamic stack. It reads the zone and the `dynamic` key from the
   state of `infra/` and creates `app.example.test` (A and TXT) with RFC2136
   updates:

   ```sh
   terraform -chdir=dynamic init
   terraform -chdir=dynamic apply
   ```

## Look around

```sh
# Through the Recursor: from PowerDNS, the transferred zone and the dynamic records.
dig @127.0.0.1 -p 15481 www.example.test
dig @127.0.0.1 -p 15481 www.legacy.test
dig @127.0.0.1 -p 15481 app.example.test TXT

# The serial of example.test on PowerDNS and on BIND.
dig @127.0.0.1 -p 15480 +short example.test SOA
dig @127.0.0.1 -p 15482 +short example.test SOA

# AXFR from PowerDNS: refused unsigned, allowed signed with bind-axfr.
dig @127.0.0.1 -p 15480 example.test AXFR
dig @127.0.0.1 -p 15480 example.test AXFR -y "$(terraform -chdir=infra output -raw bind_axfr_key)"

# Unsigned dynamic updates are refused.
printf 'server 127.0.0.1 15480\nzone example.test\nupdate add x.example.test 60 A 192.0.2.1\nsend\n' | nsupdate

# Transfers and NOTIFY in the logs.
docker compose logs pdns-auth bind | grep -E 'AXFR|NOTIFY|notify|transfer'
```

**Serials and NOTIFY:** PowerDNS increases the serial of `example.test` on
every change, through the API (the provider) and through RFC2136 updates. It
checks its primary zones every `xfr-cycle-interval` (5 seconds here, default
60) and sends NOTIFY to BIND when the serial changed; BIND then transfers the
zone and serves the same serial. PowerDNS only notifies BIND (`only-notify=`
and `also-notify` in `pdns/pdns.conf`), not the addresses of the zone's NS
records, which are on port 53.

The other direction: after editing `bind/zones/legacy.test.zone`, increase its
serial and run `docker compose exec bind rndc reload`. BIND notifies
PowerDNS, which transfers the zone. Keep the NS and glue records of the zone
file in line with `pdns_zone.legacy` in `infra/`; the provider reads them back
and would otherwise plan to change them.

## Design decisions

- **Secrets in state:** the TSIG secrets are generated with `random_bytes`
  and set with `key`, which stores them in the state of `infra/`. They have to
  be handed on, to BIND and to the dynamic stack, and a write-only `key_wo`
  (Terraform/OpenTofu >= 1.11) is not readable afterwards. Use `key_wo` for
  secrets that come from a secret store and only PowerDNS needs.
- **Keys for BIND:** `infra/` writes `bind/keys/tsig-keys.conf` with
  `local_sensitive_file`; `bind/named.conf` includes it. BIND reads it on
  start, so restart BIND after a secret changed (`docker compose restart
  bind`). The file is mode 0644, so that BIND can read it in the container,
  and ignored by git.
- **`terraform_remote_state`:** the dynamic stack reads the `infra/` outputs
  from `../infra/terraform.tfstate`. That shows how the stacks depend on each
  other, but whoever runs the dynamic stack needs read access to the infra
  state, and the dynamic state then contains the TSIG secret too. To avoid
  that, pass the key to the dynamic stack as (sensitive) variables from a
  secret store.
- **Unsigned updates:** the server runs with `dnsupdate-require-tsig=yes`
  (PowerDNS 5.0+), so a zone accepts only updates signed by one of its
  `dnsupdate` keys, and none while it has no key. `allow-dnsupdate-from` only
  lists the addresses the host's requests come from through the container
  runtime (see the comment in `pdns/pdns.conf`). The `dynamic` attachment
  uses `create_before_destroy`, so replacing it never leaves the zone without
  a key.
- **One secondary key:** PowerDNS allows one `secondary` key per zone, so the
  `legacy_secondary` attachment must not use `create_before_destroy`.
- **The Slave zone** is created with `soa.create_record = false`: its SOA and
  all records come from BIND, and the transfer happens because BIND's serial
  is newer than the default SOA PowerDNS creates. `nameservers` and `ttl` must
  match the zone file.
- **Fixed addresses:** PowerDNS takes primaries and NOTIFY targets as IP
  addresses, so the containers have fixed addresses in `10.53.0.0/24`. If that
  clashes with a local network, change it in `compose.yml`,
  `pdns/pdns.conf`, `recursor/recursor.conf`, `bind/named.conf`,
  `bind/zones/` and the variables of `infra/`.
- **DNSSEC:** the zones are unsigned and `.test` is not delegated in the
  root zone, so the Recursor has negative trust anchors for them; without
  them it answers SERVFAIL.

## Smoke test

```sh
./run.sh
TF=tofu COMPOSE="podman compose" ./run.sh
```

`run.sh` starts the containers, applies both stacks, and checks that the
Recursor resolves records of both zones, BIND serves `example.test` with the
serial of PowerDNS, PowerDNS serves `legacy.test` from BIND, signed AXFR works
and unsigned AXFR and updates are refused, the dynamic records resolve and
reach BIND, and that both plans are empty afterwards. It then destroys both
stacks, removes the containers and volumes and deletes the state. `KEEP=1`
leaves everything running; see the header of `run.sh` for the other
settings.

## Clean up

```sh
terraform -chdir=dynamic destroy
terraform -chdir=infra destroy
docker compose down --volumes
```

Destroy `dynamic/` first: it reads the key from the state of `infra/`.

## Using a local build of the provider

The stacks use the provider from the registry (`source =
"joelmuehlena/pdns"`). To use a build of this repository instead, either:

- **Terraform:** `dev_overrides` in a CLI configuration file. `terraform init`
  skips the overridden provider:

  ```sh
  go build -o "$PWD/dev-bin/" ../..   # from this directory
  cat > dev.tfrc <<EOF
  provider_installation {
    dev_overrides {
      "registry.terraform.io/joelmuehlena/pdns" = "$PWD/dev-bin"
    }
    direct {}
  }
  EOF
  export TF_CLI_CONFIG_FILE=$PWD/dev.tfrc
  ```

- **OpenTofu (and Terraform):** `tofu init` resolves overridden providers in
  the registry too, and fails as long as no released version matches. A
  filesystem mirror with the build as version 0.4.0 works for both CLIs:

  ```sh
  platform=$(go env GOOS)_$(go env GOARCH)
  for host in registry.terraform.io registry.opentofu.org; do
    go build -o "$PWD/dev-mirror/$host/joelmuehlena/pdns/0.4.0/$platform/terraform-provider-pdns_v0.4.0" ../..
  done
  cat > dev.tfrc <<EOF
  provider_installation {
    filesystem_mirror {
      path    = "$PWD/dev-mirror"
      include = ["registry.terraform.io/joelmuehlena/pdns", "registry.opentofu.org/joelmuehlena/pdns"]
    }
    direct {
      exclude = ["registry.terraform.io/joelmuehlena/pdns", "registry.opentofu.org/joelmuehlena/pdns"]
    }
  }
  EOF
  export TF_CLI_CONFIG_FILE=$PWD/dev.tfrc
  ```

Remove `.terraform/` and `.terraform.lock.hcl` of both stacks when switching
between the registry and a local build.

## Security

Everything here is for local use only: the API key (`example-api-key`), the
database password and the TSIG secrets in the state files and in
`bind/keys/tsig-keys.conf` are not protected. The API accepts requests from
every address that reaches its port (published on 127.0.0.1). In a real
setup, keep the API key and the TSIG secrets in a secret store, use a remote
state backend with encryption and access control, and keep
`allow-dnsupdate-from` and `webserver-allow-from` as narrow as possible.
