# Terraform / OpenTofu Provider for PowerDNS

Manage [PowerDNS](https://www.powerdns.com/) Authoritative Server resources —
zones and records — through its HTTP API using Terraform or OpenTofu.

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

See the [`docs/`](./docs) directory for the full resource reference.

## Development

```sh
make lint      # golangci-lint
make generate  # regenerate docs (requires tfplugindocs)
go build ./...
```
