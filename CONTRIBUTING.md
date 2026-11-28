# Contributing

## Tools and tasks

Tools and tasks are managed with [mise](https://mise.jdx.dev/); the versions
are pinned in [`mise.toml`](mise.toml).

```sh
mise install       # install go, golangci-lint, terraform, opentofu, goreleaser, oras
mise run build     # build the provider
mise run vet       # go vet
mise run test      # unit tests
mise run lint      # golangci-lint
mise run generate  # format examples and regenerate docs
mise run testacc   # acceptance tests against PowerDNS in docker
mise run oci       # goreleaser snapshot, pushed as an OCI mirror artifact
```

`docs/` is generated from the schema descriptions, `examples/` and
`templates/`. Run `mise run generate` and commit the result; CI fails on drift.

## Acceptance tests

The acceptance tests (`internal/provider/*_acc_test.go`, `TestAcc*`) run the
resources against a real PowerDNS Authoritative Server. They are skipped
unless `TF_ACC=1`, `PDNS_ACC_ENDPOINT` and `PDNS_ACC_API_KEY` are set;
`PDNS_ACC_DNS_ADDR` (host:port of the DNS listener) additionally enables the
RFC2136 update tests.

[`test/acc/run.sh`](test/acc/run.sh) starts a server with docker compose, runs
the tests and tears the server down. Arguments go to `go test`:

```sh
mise run testacc                                   # PowerDNS 5.1, gsqlite3
PDNS_VERSION=4.9 PDNS_BACKEND=gpgsql mise run testacc -- -v
COMPOSE="podman compose" mise run testacc          # podman instead of docker
PDNS_ACC_SERVER_ARGS=--dnsupdate-require-tsig=yes mise run testacc  # extra server flags
TF_ACC_TERRAFORM_PATH="$(mise which tofu)" TF_ACC_PROVIDER_HOST=registry.opentofu.org \
  mise run testacc                                 # OpenTofu instead of Terraform
```

`TestAccDNSUpdateRequireTSIG` only runs against a server with
`dnsupdate-require-tsig=yes` (PowerDNS 5.0+). See `test/acc/run.sh` for all
settings, e.g. the ports and the compose project name.

The PowerDNS and PostgreSQL images are pinned to exact versions in the `FROM`
lines of `test/acc/images/*.Dockerfile`, which `run.sh` reads (they are never
built). Dependabot proposes updates; to bump one by hand, edit its `FROM`
line. `PDNS_IMAGE` overrides the image for one run, e.g.
`PDNS_IMAGE=docker.io/powerdns/pdns-auth-51:latest`.

A new backend needs a `test/acc/compose.<backend>.yml` (plus its config) and
an entry in the `acceptance` matrix of `.github/workflows/ci.yml`. A new
PowerDNS series needs a `test/acc/images/pdns-auth-<series>.Dockerfile` and a
matrix entry, and `minTestedVersion` and `maxTestedVersion` in
`internal/pdns_client/pdns_client.go` must match the support matrix in the
README.

## Releases and the OCI artifact

Pushing a `v*` tag runs `.github/workflows/release.yml`: goreleaser builds and
GPG-signs the release, and [`scripts/oci-publish.sh`](scripts/oci-publish.sh)
publishes its zips as the OCI artifact described in
[docs/guides/oci.md](docs/guides/oci.md), which is then signed with cosign
and attested.

`mise run oci` runs the script on a goreleaser snapshot (`dist/`) and pushes
to `$OCI_REPOSITORY`, by default `localhost:5000/terraform-provider-pdns`.
Arguments go to `oras cp`:

```sh
mise run oci -- --to-plain-http   # local registry without TLS (inspect only)
OCI_REPOSITORY=registry.test:5443/terraform-provider-pdns \
  mise run oci -- --to-ca-file ca.pem
```

`tofu init` needs the registry on HTTPS, and a snapshot version such as
`0.3.0-SNAPSHOT-f06c1c6` is a pre-release, so pin it exactly.

The index digest is reproducible from the zips, the version,
`SOURCE_DATE_EPOCH` (the `org.opencontainers.image.created` annotation) and
`OCI_REVISION` (the full commit SHA in `org.opencontainers.image.revision`).
The script defaults both to the commit of the tag `v<version>`, as the release
workflow does; `mise run oci` uses `HEAD`. To check a release, rebuild it from
its assets in a clone with the tags and compare the digests:

```sh
gh release download v0.4.0 --dir rel --pattern '*.zip' --pattern '*_SHA256SUMS'
scripts/oci-publish.sh rel v0.4.0 rebuilt --to-oci-layout   # prints rebuilt@sha256:...
oras resolve ghcr.io/joelmuehlena/terraform-provider-pdns:0.4.0
```

The digests match as long as the oras version in `mise.toml` is the one the
release used.
