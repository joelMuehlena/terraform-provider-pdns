# Terraform / OpenTofu Provider for PowerDNS

Manage [PowerDNS](https://www.powerdns.com/) Authoritative Server resources —
zones, records and TSIG keys — through its HTTP API using Terraform or OpenTofu.

## Requirements

- Terraform or OpenTofu; see the [support matrix](#support-matrix) for the tested versions.
- A reachable PowerDNS Authoritative Server with the [HTTP API](https://doc.powerdns.com/authoritative/http-api/index.html) enabled and an API key

## Support matrix

<!-- Keep minTestedVersion and maxTestedVersion in internal/pdns_client/pdns_client.go in sync with the PowerDNS row. -->

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

After its first request has verified `server_id`, the provider logs the PowerDNS
server it talks to, and warns if its version is outside the [support matrix](#support-matrix):

```text
[INFO]  provider.terraform-provider-pdns_v1.2.3: Connected to PowerDNS: daemon_type=authoritative endpoint=https://pdns-auth.example.com server_id=localhost version=5.1.2 ...
```

Requests to PowerDNS carry the build and the CLI version in their `User-Agent`, e.g.
`terraform-provider-pdns/1.2.3 (commit 0123456789ab) terraform/1.17.2` (with OpenTofu
the OpenTofu version), so that a reverse proxy in front of PowerDNS, or PowerDNS itself
with `webserver-loglevel=detailed` and `loglevel=6`, logs which client sent a request.
`detailed` also logs every request header including `X-API-Key`, so only enable it
for debugging.

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

## Installing from an OCI registry (OpenTofu)

Every release is also published as an OpenTofu
[OCI provider mirror](https://opentofu.org/docs/cli/oci_registries/provider-mirror/)
artifact at `ghcr.io/joelmuehlena/terraform-provider-pdns`, so you can install
the provider from ghcr.io or copy it into your own OCI registry. This needs
OpenTofu >= 1.10; Terraform does not support OCI mirrors.

- The tag is the exact version without the leading `v`, e.g. `0.4.0`, and
  `1.2.0-rc.1` for a pre-release. There is no `latest` or any other floating
  tag. OCI tags cannot contain `+`, so build metadata uses `_`
  (`1.2.0+build.1` is tagged `1.2.0_build.1`), as OpenTofu expects.
- The artifact is an image index with one manifest per release platform, each
  holding the unchanged zip of the GitHub release as its only layer; the
  layer digest is the SHA-256 listed in the release's `_SHA256SUMS` file.
- The digest is reproducible: the index is built only from the release zips,
  the version and the date of the tagged commit (its
  `org.opencontainers.image.created` annotation), so anyone can rebuild it
  from the release assets and compare, see
  [OCI artifact](#oci-artifact) under Development.

Point the `joelmuehlena/pdns` provider at the mirror in the
[CLI configuration](https://opentofu.org/docs/cli/config/config-file/)
(`~/.tofurc`, or the file `TF_CLI_CONFIG_FILE` names) and keep `direct` for
every other provider:

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

The module configuration does not change: it keeps
`source = "joelmuehlena/pdns"`, and `tofu init` reports
`Installed joelmuehlena/pdns v0.4.0 (verified checksum)`. The `zh:` hash it
writes to `.terraform.lock.hcl` is the zip's SHA-256, the same hash the
OpenTofu registry records, so lock files work with either source.

- **Pre-releases:** as with the registry, OpenTofu selects a pre-release only
  when the version constraint names it exactly (`version = "1.2.0-rc.1"`);
  ranges such as `>= 1.2.0` or `~> 1.2` skip them.
- **Credentials:** pulling the public ghcr.io package needs none. For a
  private registry OpenTofu uses the credentials of `docker login`,
  `podman login` or `oras login`, see
  [OCI Registry Credentials](https://opentofu.org/docs/cli/oci_registries/credentials/).
- **TLS:** OpenTofu talks to OCI registries over HTTPS only and has no
  plain-HTTP option. For a registry with a private CA, add the CA to the
  system trust store or point `SSL_CERT_FILE` at it.

### Mirroring into your own registry

Copy a version with [ORAS](https://oras.land/) (>= 1.3.0) and use your
registry in `repository_template`:

```sh
oras cp -r ghcr.io/joelmuehlena/terraform-provider-pdns:0.4.0 \
  registry.example.com/opentofu/terraform-provider-pdns:0.4.0
```

```hcl
    repository_template = "registry.example.com/opentofu/terraform-provider-${type}"
```

`-r` also copies the artifacts that refer to it, the signature and the
attestation (see [Verifying the OCI artifact](#verifying-the-oci-artifact)). Without
direct access from the target network, copy through an
[OCI image layout](https://github.com/opencontainers/image-spec/blob/main/image-layout.md)
directory:

```sh
# connected machine
oras cp -r ghcr.io/joelmuehlena/terraform-provider-pdns:0.4.0 \
  --to-oci-layout terraform-provider-pdns:0.4.0
# carry the terraform-provider-pdns directory across, then
oras cp -r --from-oci-layout terraform-provider-pdns:0.4.0 \
  registry.internal.example/opentofu/terraform-provider-pdns:0.4.0
```

### Verifying the OCI artifact

Each release can be verified with cosign (below) and with its GitHub build
provenance attestation (see
[Verifying the build provenance](#verifying-the-build-provenance)).

The release workflow signs the index of each OCI artifact by digest with
[cosign](https://docs.sigstore.dev/cosign/) keyless signing: the certificate
is issued by Sigstore's Fulcio for the GitHub OIDC identity of the release
workflow run for the tag, and the signature is recorded in the Rekor
transparency log. The signature is a Sigstore bundle attached to the index as
an OCI referrer (on ghcr.io under the tag `sha256-<index digest>`, which
OpenTofu ignores as it is not a version). The index names every platform
manifest, and each manifest its zip, by digest, so verifying the index
covers all platforms.

OpenTofu does not verify cosign signatures: for an OCI mirror it checks the
zip against the digest in the manifest (`verified checksum`), and its OCI
mirror docs describe no signature verification. Signing is therefore
optional for consumers, and `tofu init` works the same with or without it.
To rely on it, verify before you mirror or install. With cosign 3:

```sh
cosign verify \
  --certificate-identity "https://github.com/joelMuehlena/terraform-provider-pdns/.github/workflows/release.yml@refs/tags/v0.4.0" \
  --certificate-oidc-issuer https://token.actions.githubusercontent.com \
  ghcr.io/joelmuehlena/terraform-provider-pdns:0.4.0
```

The identity is case-sensitive and names the tag with its `v`. To accept any
release tag, use
`--certificate-identity-regexp '^https://github\.com/joelMuehlena/terraform-provider-pdns/\.github/workflows/release\.yml@refs/tags/v'`
instead. cosign resolves the tag to a digest; to be sure that you mirror what
you verified, verify and copy the same digest
(`oras resolve ghcr.io/joelmuehlena/terraform-provider-pdns:0.4.0`).

`oras cp -r` (see above) copies the signature along with the artifact, so the
same `cosign verify` works against your registry. (`cosign copy` is
deprecated in cosign 3 in favour of `oras cp -r`.)

#### Air-gapped verification

The verifying side needs neither ghcr.io nor Sigstore's services: the bundle
holds the certificate, the signature and the Rekor inclusion proof, and the
Sigstore trusted root (Fulcio CA certificates and the Rekor and CT log keys)
can be carried across as a file.

On a connected machine:

```sh
# Fetch the Sigstore trusted root through TUF
# (cached in ~/.sigstore/root, or in $TUF_ROOT if set).
cosign initialize
cp ~/.sigstore/root/tuf-repo-cdn.sigstore.dev/targets/trusted_root.json .

# Optionally verify here first (cosign verify ... as above), then save the
# artifact together with its signature.
oras cp -r ghcr.io/joelmuehlena/terraform-provider-pdns:0.4.0 \
  --to-oci-layout terraform-provider-pdns:0.4.0
oras resolve --oci-layout terraform-provider-pdns:0.4.0   # note the digest
```

Carry the `terraform-provider-pdns` directory and `trusted_root.json` across,
then push the artifact to the internal registry and verify it there:

```sh
oras cp -r --from-oci-layout terraform-provider-pdns:0.4.0 \
  registry.internal.example/opentofu/terraform-provider-pdns:0.4.0
cosign verify \
  --trusted-root trusted_root.json \
  --certificate-identity "https://github.com/joelMuehlena/terraform-provider-pdns/.github/workflows/release.yml@refs/tags/v0.4.0" \
  --certificate-oidc-issuer https://token.actions.githubusercontent.com \
  registry.internal.example/opentofu/terraform-provider-pdns@sha256:<digest>
```

With `--trusted-root`, cosign does not contact TUF, Fulcio or Rekor; without
it, it tries to fetch the trusted root from `tuf-repo-cdn.sigstore.dev` and
fails offline. cosign 3 has no `--offline` flag; the bundle's inclusion proof
is verified offline anyway. Refresh `trusted_root.json` from time to time:
it must contain the keys that were valid when a release was signed, and
Sigstore adds new keys to it when it rotates them.

### Verifying the build provenance

The release workflow also creates a
[GitHub artifact attestation](https://docs.github.com/en/actions/security-guides/using-artifact-attestations-to-establish-provenance-for-builds)
(SLSA build provenance, signed with Sigstore for the release workflow's
identity) for the index of the OCI artifact and for every release asset
listed in `_SHA256SUMS`. It records the repository, the workflow, the tag and
the commit the release was built from. Verify it with the
[GitHub CLI](https://cli.github.com/manual/gh_attestation_verify)
(`gh auth login` first). The public ghcr.io package needs no registry
login; for other registries `gh` uses the credentials of `docker login`.

```sh
gh attestation verify oci://ghcr.io/joelmuehlena/terraform-provider-pdns:0.4.0 \
  --repo joelMuehlena/terraform-provider-pdns
gh attestation verify terraform-provider-pdns_0.4.0_linux_amd64.zip \
  --repo joelMuehlena/terraform-provider-pdns
```

`--owner joelMuehlena` instead of `--repo` accepts any repository of the
owner. To pin the workflow and the tag as well, add
`--signer-workflow joelMuehlena/terraform-provider-pdns/.github/workflows/release.yml`
and `--source-ref refs/tags/v0.4.0`. `--format json` prints the verified
statement, e.g. for a policy engine.

The attestation of the OCI artifact is stored in two places: with GitHub's
attestations API, where `gh attestation verify` looks by default, and as a
Sigstore bundle referrer of the index in the registry, next to the cosign
signature. `oras cp -r` copies it along when mirroring, and
`--bundle-from-oci` reads it from there instead of from GitHub.

Air-gapped, `gh attestation verify` needs neither GitHub nor Sigstore when
it gets the attestation and the trusted root as files or from the registry:

```sh
# connected machine
gh attestation trusted-root > trusted_root.jsonl
gh attestation download oci://ghcr.io/joelmuehlena/terraform-provider-pdns:0.4.0 \
  --repo joelMuehlena/terraform-provider-pdns   # writes sha256:<digest>.jsonl
```

Carry `trusted_root.jsonl` (and the `.jsonl` bundle, or the mirrored
artifact with `oras cp -r` as above) across, then:

```sh
gh attestation verify oci://registry.internal.example/opentofu/terraform-provider-pdns@sha256:<digest> \
  --repo joelMuehlena/terraform-provider-pdns \
  --bundle-from-oci --custom-trusted-root trusted_root.jsonl
# or, with the downloaded bundle: --bundle sha256:<digest>.jsonl --custom-trusted-root trusted_root.jsonl
```

Without `--bundle` or `--bundle-from-oci`, `gh` asks the GitHub API for the
attestation, and without `--custom-trusted-root` it fetches the trusted root
from Sigstore's TUF repository; both fail offline. With a bundle, `--repo`
and `--owner` are compared with the names recorded in the certificate; only
the online lookup notices that a repository has since been renamed or
replaced. As with cosign, refresh `trusted_root.jsonl` from time to time.

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
mise run oci       # goreleaser snapshot, pushed as an OCI mirror artifact
```

### OCI artifact

`scripts/oci-publish.sh` builds the OCI provider mirror artifact from a
directory of release zips and pushes it; the release workflow runs it on the
assets of each GitHub release. `mise run oci` runs it on a goreleaser
snapshot (`dist/`) and pushes to `$OCI_REPOSITORY`, by default
`localhost:5000/terraform-provider-pdns`; arguments go to `oras cp`:

```sh
mise run oci -- --to-plain-http   # local registry without TLS (inspect only)
OCI_REPOSITORY=registry.test:5443/terraform-provider-pdns \
  mise run oci -- --to-ca-file ca.pem
```

`tofu init` needs the registry on HTTPS (see above), and a snapshot version
such as `0.3.0-SNAPSHOT-f06c1c6` is a pre-release, so pin it exactly.

The artifact is reproducible from its inputs: the zips, the version and
`SOURCE_DATE_EPOCH`, which sets the `org.opencontainers.image.created`
annotation (the only time in it; oras would use the current time). The
script defaults it to the committer date of the tag `v<version>`, which is
what the release workflow passes; `mise run oci` uses the date of `HEAD`.
To check a release, rebuild it into a local OCI layout from its assets in a
clone with the tags and compare the digest with the registry's:

```sh
gh release download v0.4.0 --dir rel --pattern '*.zip' --pattern '*_SHA256SUMS'
scripts/oci-publish.sh rel v0.4.0 rebuilt --to-oci-layout   # prints rebuilt@sha256:...
oras resolve ghcr.io/joelmuehlena/terraform-provider-pdns:0.4.0
```

Both digests match as long as the oras version (`mise.toml`) is the one the
release used.

CI runs the same on every pull request and push to `main`: the `oci` job
pushes a snapshot with `mise run oci` to a registry with a throwaway CA,
pushes it again to check that the digest does not change, and installs it
with `tofu init` through an `oci_mirror`.

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
