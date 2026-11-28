# Terraform / OpenTofu Provider for PowerDNS

Manage [PowerDNS](https://www.powerdns.com/) Authoritative Server resources —
zones, records and TSIG keys — through its HTTP API using Terraform or OpenTofu.

## Requirements

- Terraform or OpenTofu; see the [support matrix](#support-matrix) for the tested versions.
- A reachable PowerDNS Authoritative Server with the [HTTP API](https://doc.powerdns.com/authoritative/http-api/index.html) enabled and an API key

## Support matrix

| Component                      | Supported                  | Tested in CI                    |
| ------------------------------ | -------------------------- | ------------------------------- |
| Terraform                      | >= 1.0                     | 1.15, 1.16, 1.17                |
| OpenTofu                       | >= 1.6                     | 1.11, 1.12, 1.13                |
| PowerDNS Authoritative Server  | 4.9, 5.0, 5.1              | 4.9, 5.0, 5.1                   |
| PowerDNS database backends     | `gsqlite3`, `gpgsql`       | `gsqlite3`, `gpgsql`            |

- The provider uses plugin protocol 6, so it works with any Terraform or OpenTofu version that speaks it. CI covers the three latest minor versions of each CLI; older versions within the supported range are not tested.
- Every PowerDNS version is tested against both backends in the table. Other backends use the same HTTP API but are not covered by CI.
- Write-only attributes such as `pdns_tsig_key.key_wo` need Terraform or OpenTofu >= 1.11.
- PowerDNS versions before 4.9 are not tested. 4.9 lacks `dnsupdate-require-tsig`; see [Securing dynamic updates](#securing-dynamic-updates) for `lock_dnsupdate`.

## Usage

```terraform
terraform {
  required_providers {
    pdns = {
      source = "joelmuehlena/pdns"
    }
  }
}

provider "pdns" {
  endpoint = "https://pdns-auth.example.com"
  api_key  = "someApiKey"
}

resource "pdns_zone" "example_com" {
  name = "example.com."

  # TTL of the SOA, NS and glue records (soa.ttl is the negative caching TTL).
  ttl = 3600

  nameservers = [
    # Glue records: dual-stack (A and AAAA) for ns1, IPv4 only for ns2.
    { hostname = "ns1", addresses = ["10.10.10.1", "2001:db8::1"] },
    { hostname = "ns2", addresses = ["10.10.10.2"] },
    # Outside the zone, so without glue records.
    { hostname = "ns.example.net." },
  ]

  soa = {
    rname   = "hostmaster"
    refresh = 10800
    retry   = 3600
    expire  = 604800
    ttl     = 3600
  }
}

resource "pdns_record" "www" {
  zone = pdns_zone.example_com.name
  name = "www"
  type = "A"

  records = ["10.10.10.4"]
}
```

## Provider configuration

| Argument          | Required | Description                                                                 |
| ----------------- | -------- | --------------------------------------------------------------------------- |
| `endpoint`        | yes      | Base URL of the PowerDNS Authoritative API server.                          |
| `api_key`         | yes      | API key used for the `X-API-Key` header (sensitive).                        |
| `server_id`       | no       | Server id. Defaults to `localhost`.                                         |
| `skip_tls_verify` | no       | Skip verification of the remote's TLS certificate. Defaults to `false`.     |
| `timeout`         | no       | Timeout of each API request as a Go duration (`0s` disables it). Defaults to `30s`. |

The provider honors the `HTTPS_PROXY`, `HTTP_PROXY` and `NO_PROXY` environment variables.

### Logging and version

Set `TF_LOG=INFO` (or `TF_LOG_PROVIDER=INFO` for the provider's logs only, `DEBUG` for
every API request) to see which provider build runs. When the provider is configured
it logs a line like:

```text
[INFO]  provider.terraform-provider-pdns_v1.2.3: Starting PowerDNS provider: build="1.2.3 (commit 0123456789ab, 2026-01-02T03:04:05Z, go1.27.4 linux/amd64)" commit=0123456789abcdef0123456789abcdef01234567 version=1.2.3 ...
```

Release builds carry the version and commit of the release. Binaries built with
`go build` or `go install` (e.g. for `dev_overrides`) take them from the build
information Go embeds: the module version if known (else `dev`), the git commit and
`-dirty` for a working tree with uncommitted changes.

## Resources

| Resource        | Description                                                                                   |
| --------------- | --------------------------------------------------------------------------------------------- |
| `pdns_zone`     | A zone including its SOA and NS records and the A/AAAA glue records of its nameservers.       |
| `pdns_record`   | A single rrset within a zone.                                                                 |
| `pdns_tsig_key` | A TSIG key. The secret is set via `key` or the write-only `key_wo` (Terraform/OpenTofu >= 1.11). |
| `pdns_zone_tsig_key` | Attaches a TSIG key to a zone for `dnsupdate`, `axfr` and/or `secondary`. Many keys per zone and many zones per key. |

See the [`docs/`](./docs) directory for the full resource reference.

The `address` attribute of `pdns_zone` nameservers is deprecated and will be
removed in a future major version. Replace `address = "192.0.2.1"` with
`addresses = ["192.0.2.1"]`; with the same address, the switch changes no glue
record. `addresses` also takes several addresses, e.g. an IPv4 and an IPv6
address for dual-stack glue, and is what `terraform import` fills in.

## Securing dynamic updates

By default PowerDNS only requires a TSIG signature for RFC2136 updates while a zone's `TSIG-ALLOW-DNSUPDATE` lists a key. A zone without dnsupdate keys (a new zone, or after the last `pdns_zone_tsig_key` with `dnsupdate = true` is removed) accepts **unsigned** updates from every address in `allow-dnsupdate-from`.

- **PowerDNS 5.0 and later:** run the server with `dnsupdate-require-tsig=yes`. Updates must then be signed by a key listed for the zone. A zone without keys refuses all updates, including validly signed ones by other keys, from the moment it exists.
- **PowerDNS 4.9**, or servers whose configuration you can't change: set `lock_dnsupdate = true` on `pdns_zone`. The zone keeps a placeholder key in `TSIG-ALLOW-DNSUPDATE`, and new zones are created as `Slave`, locked, then switched to their kind, so they are never unlocked. 4.9 doesn't know `dnsupdate-require-tsig` and refuses to start with it.
  - The placeholder is named `pdns-provider-dnsupdate-locked`. `pdns_tsig_key` refuses that name, but nothing stops a key with it from being created outside the provider (`pdnsutil generate-tsig-key`, `pdnsutil import-tsig-key` or the API), and such a key could sign updates of **every** locked zone. Never create one, and check that `pdnsutil list-tsig-keys` doesn't list it.
- **Per-zone control, also on 5.0+:** `dnsupdate-require-tsig` applies to every zone of the server. If some zones must keep accepting unsigned updates (from `allow-dnsupdate-from`) while others must not, leave the setting off and set `lock_dnsupdate = true` only on the zones that must refuse them.
- **Replacing an attachment:** changing `zone` or `tsig_key_id` of a `pdns_zone_tsig_key` replaces it, destroying the old attachment first. If it held the zone's only dnsupdate key, the zone accepts unsigned updates until the new one exists, unless one of the settings above is in place. Set `lifecycle { create_before_destroy = true }` on attachments with `dnsupdate = true` (not with `secondary = true`, see the [resource docs](docs/resources/zone_tsig_key.md)).
- In all cases, keep `allow-dnsupdate-from` as narrow as possible. The default is `127.0.0.0/8,::1`.

## Development

Tools and tasks are managed with [mise](https://mise.jdx.dev/):

```sh
mise install       # install pinned go, golangci-lint, terraform, opentofu
mise run build     # build the provider
mise run vet       # go vet
mise run test      # unit tests
mise run lint      # golangci-lint
mise run generate  # format examples and regenerate docs
mise run testacc   # acceptance tests against PowerDNS in docker
```

### Acceptance tests

The acceptance tests (`internal/provider/*_acc_test.go`, `TestAcc*`) run the
resources against a real PowerDNS Authoritative server. They are skipped
unless `TF_ACC=1`, `PDNS_ACC_ENDPOINT` and `PDNS_ACC_API_KEY` are set;
`PDNS_ACC_DNS_ADDR` (host:port of the DNS listener) additionally enables the
RFC2136 update tests.

`test/acc/run.sh` starts a server with docker compose, runs the tests and
tears the server down:

```sh
mise run testacc                                   # PowerDNS 5.1, gsqlite3
PDNS_VERSION=4.9 PDNS_BACKEND=gpgsql mise run testacc -- -v
COMPOSE="podman compose" mise run testacc          # podman instead of docker
PDNS_ACC_SERVER_ARGS=--dnsupdate-require-tsig=yes mise run testacc  # extra server flags
TF_ACC_TERRAFORM_PATH="$(mise which tofu)" TF_ACC_PROVIDER_HOST=registry.opentofu.org \
  mise run testacc                                 # OpenTofu instead of Terraform
```

The PowerDNS and PostgreSQL images are pinned to exact versions in
`test/acc/images/*.Dockerfile` (version manifests read by `run.sh`, never
built). Dependabot proposes updates for them; to bump one by hand, edit its
`FROM` line. `PDNS_IMAGE` overrides the image for one run, e.g.
`PDNS_IMAGE=docker.io/powerdns/pdns-auth-51:latest`.

`TestAccDNSUpdateRequireTSIG` only runs against a server with
`dnsupdate-require-tsig=yes` (PowerDNS 5.0+). See `test/acc/run.sh` for all
settings, e.g. the ports and the compose project name.

CI runs PowerDNS 4.9, 5.0 and 5.1, each with the `gsqlite3` and `gpgsql`
backends, plus 5.0 and 5.1 with `dnsupdate-require-tsig=yes`, all with
Terraform, and 5.1 with both backends with OpenTofu. Another backend
is one `test/acc/compose.<backend>.yml` (plus its config) and one matrix entry.
