# Terraform / OpenTofu Provider for PowerDNS

Manage [PowerDNS](https://www.powerdns.com/) Authoritative Server zones, records
and TSIG keys through its [HTTP API](https://doc.powerdns.com/authoritative/http-api/index.html)
with Terraform or OpenTofu. Published on the
[Terraform Registry](https://registry.terraform.io/providers/joelMuehlena/pdns/latest)
and the [OpenTofu Registry](https://search.opentofu.org/provider/joelmuehlena/pdns/latest).

## Support matrix

<!-- Keep minTestedVersion and maxTestedVersion in internal/pdns_client/pdns_client.go in sync with the PowerDNS row. -->

| Component                     | Supported            | Tested in CI         |
| ----------------------------- | -------------------- | -------------------- |
| Terraform                     | >= 1.0               | 1.15, 1.16, 1.17     |
| OpenTofu                      | >= 1.6               | 1.11, 1.12, 1.13     |
| PowerDNS Authoritative Server | 4.9, 5.0, 5.1        | 4.9, 5.0, 5.1        |
| PowerDNS database backends    | `gsqlite3`, `gpgsql` | `gsqlite3`, `gpgsql` |

- The unit tests run with each listed CLI version. The acceptance tests run
  against every PowerDNS version and backend with Terraform 1.17, and against
  PowerDNS 5.1 (both backends) with OpenTofu 1.13.
- Other backends use the same HTTP API but are not tested. The provider warns
  when it connects to a PowerDNS version outside the tested range.
- Write-only attributes (`pdns_tsig_key.key_wo`) need Terraform or OpenTofu >= 1.11.
- With `gpgsql` before PowerDNS 5.1, changing the secret of an existing TSIG
  key fails with HTTP 500; see [`pdns_tsig_key`](docs/resources/tsig_key.md)
  for the workaround.

## Usage

```terraform
terraform {
  required_providers {
    pdns = {
      source = "joelmuehlena/pdns"
    }
  }
}

variable "pdns_api_key" {
  type      = string
  sensitive = true
}

provider "pdns" {
  endpoint = "https://pdns-auth.example.com"
  api_key  = var.pdns_api_key
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

The provider arguments (`endpoint`, `api_key`, `server_id`, `skip_tls_verify`,
`timeout`) are described in [docs/index.md](docs/index.md). The provider honors
the `HTTPS_PROXY`, `HTTP_PROXY` and `NO_PROXY` environment variables.

## Resources

| Resource             | Manages                                                                               |
| -------------------- | ------------------------------------------------------------------------------------- |
| `pdns_zone`          | A zone with its SOA and NS records and the A/AAAA glue records of its nameservers.    |
| `pdns_record`        | One rrset within a zone.                                                              |
| `pdns_tsig_key`      | A TSIG key, with the secret in `key` or the write-only `key_wo`.                      |
| `pdns_zone_tsig_key` | The attachment of a TSIG key to a zone for `dnsupdate`, `axfr` and/or `secondary`.    |

Every resource can be imported. See [`docs/resources/`](docs/resources) for the
full reference.

## Securing dynamic updates

PowerDNS requires a TSIG signature for RFC2136 updates only while the zone's
`TSIG-ALLOW-DNSUPDATE` lists a key. A zone without dnsupdate keys (a new zone,
or after its last `pdns_zone_tsig_key` with `dnsupdate = true` is removed)
accepts **unsigned** updates from every address in `allow-dnsupdate-from`.

- **PowerDNS 5.0 and later:** run the server with `dnsupdate-require-tsig=yes`.
  A zone then accepts only updates signed by one of its keys, and none while
  it has no key.
- **PowerDNS 4.9, a server you can't reconfigure, or per-zone control:** set
  [`lock_dnsupdate = true`](docs/resources/zone.md) on each `pdns_zone` that
  must refuse unsigned updates (for per-zone control on 5.0+, leave
  `dnsupdate-require-tsig` off). 4.9 does not know `dnsupdate-require-tsig`
  and refuses to start with it. Never create a TSIG key named
  `pdns-provider-dnsupdate-locked` outside the provider: it could sign updates
  of every locked zone.
- **Replacing an attachment** (changing `zone` or `tsig_key_id`) destroys the
  old one first. If it held the zone's only dnsupdate key, the zone accepts
  unsigned updates until the new one exists, unless one of the settings above
  is in place. Set `lifecycle { create_before_destroy = true }` on
  attachments with `dnsupdate = true`; see
  [`pdns_zone_tsig_key`](docs/resources/zone_tsig_key.md) for the caveats.
- Keep `allow-dnsupdate-from` as narrow as possible (default `127.0.0.0/8,::1`).

## Installing from an OCI registry (OpenTofu)

Releases after v0.3.0 are also published as an OpenTofu
[OCI provider mirror](https://opentofu.org/docs/cli/oci_registries/provider-mirror/)
artifact at `ghcr.io/joelmuehlena/terraform-provider-pdns`, tagged with the
exact version (e.g. `0.4.0`). This needs OpenTofu >= 1.10; Terraform does not
support OCI mirrors. Add to the
[CLI configuration](https://opentofu.org/docs/cli/config/config-file/):

```hcl
provider_installation {
  oci_mirror {
    repository_template = "ghcr.io/joelmuehlena/terraform-provider-${type}"
    include             = ["registry.opentofu.org/joelmuehlena/pdns"]
  }
  direct {
    exclude = ["registry.opentofu.org/joelmuehlena/pdns"]
  }
}
```

The module keeps `source = "joelmuehlena/pdns"`, and lock files work with
either source. Copying into a private or air-gapped registry, the release
signatures and the build provenance are covered in
[docs/guides/oci.md](docs/guides/oci.md).

## Troubleshooting

`TF_LOG_PROVIDER=INFO` shows the provider build (`Starting PowerDNS provider`)
and, after the first request, the PowerDNS server and version it talks to
(`Connected to PowerDNS`), with a warning if the version is untested.
`TF_LOG_PROVIDER=DEBUG` logs every API request. Requests carry a `User-Agent`
such as `terraform-provider-pdns/1.2.3 (commit 0123456789ab) terraform/1.17.2`
(`terraform/` followed by the CLI version, also for OpenTofu). PowerDNS logs
it with `webserver-loglevel=detailed` and `loglevel=6`, but `detailed` also
logs the `X-API-Key` header, so only enable it for debugging.

## Contributing

See [CONTRIBUTING.md](CONTRIBUTING.md). Licensed under the [Apache License 2.0](LICENSE).
