# Terraform / OpenTofu Provider for PowerDNS

Manage [PowerDNS](https://www.powerdns.com/) Authoritative Server resources —
zones, records and TSIG keys — through its HTTP API using Terraform or OpenTofu.

## Requirements

- Terraform >= 1.0 or OpenTofu >= 1.6
- A reachable PowerDNS Authoritative Server with the [HTTP API](https://doc.powerdns.com/authoritative/http-api/index.html) enabled and an API key

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

  nameservers = [
    { hostname = "ns1", address = "10.10.10.1" },
    { hostname = "ns2", address = "10.10.10.2" },
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

## Resources

| Resource        | Description                                                                                   |
| --------------- | --------------------------------------------------------------------------------------------- |
| `pdns_zone`     | A zone including its SOA and NS records.                                                      |
| `pdns_record`   | A single rrset within a zone.                                                                 |
| `pdns_tsig_key` | A TSIG key. The secret is set via `key` or the write-only `key_wo` (Terraform/OpenTofu >= 1.11). |
| `pdns_zone_tsig_key` | Attaches a TSIG key to a zone for `dnsupdate`, `axfr` and/or `secondary`. Many keys per zone and many zones per key. |

See the [`docs/`](./docs) directory for the full resource reference.

## Securing dynamic updates

By default PowerDNS only requires a TSIG signature for RFC2136 updates while a zone's `TSIG-ALLOW-DNSUPDATE` lists a key. A zone without dnsupdate keys (a new zone, or after the last `pdns_zone_tsig_key` with `dnsupdate = true` is removed) accepts **unsigned** updates from every address in `allow-dnsupdate-from`.

- **PowerDNS 5.0 and later:** run the server with `dnsupdate-require-tsig=yes`. Updates must then be signed by a key listed for the zone. A zone without keys refuses all updates, including validly signed ones by other keys, from the moment it exists.
- **PowerDNS 4.9**, or servers whose configuration you can't change: set `lock_dnsupdate = true` on `pdns_zone`. The zone keeps a placeholder key in `TSIG-ALLOW-DNSUPDATE`, and new zones are created as `Slave`, locked, then switched to their kind, so they are never unlocked. 4.9 doesn't know `dnsupdate-require-tsig` and refuses to start with it.
- **Per-zone control, also on 5.0+:** `dnsupdate-require-tsig` applies to every zone of the server. If some zones must keep accepting unsigned updates (from `allow-dnsupdate-from`) while others must not, leave the setting off and set `lock_dnsupdate = true` only on the zones that must refuse them.
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
signed RFC2136 update test.

`test/acc/run.sh` starts a server with docker compose, runs the tests and
tears the server down:

```sh
mise run testacc                                   # PowerDNS 5.1, gsqlite3
PDNS_VERSION=4.9 PDNS_BACKEND=gpgsql mise run testacc -- -v
COMPOSE="podman compose" mise run testacc          # podman instead of docker
```

CI runs PowerDNS 4.9, 5.0 and 5.1, each with the `gsqlite3` and `gpgsql`
backends. Another backend is one `test/acc/compose.<backend>.yml` (plus its
config) and one matrix entry.
