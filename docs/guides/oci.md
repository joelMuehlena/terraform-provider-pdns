---
page_title: "OCI registries and release verification"
subcategory: ""
description: |-
  Install the provider with OpenTofu from ghcr.io or your own OCI registry, also air-gapped, and verify release checksums, signatures and build provenance.
---

# OCI registries and release verification

Releases after v0.3.0 are also published as an OpenTofu
[OCI provider mirror](https://opentofu.org/docs/cli/oci_registries/provider-mirror/)
artifact at `ghcr.io/joelmuehlena/terraform-provider-pdns`. The examples use
version `0.4.0`; replace it with the version you install.

## Installing from an OCI registry

This needs OpenTofu >= 1.10; Terraform does not support OCI mirrors.

- Tags are the exact version without the leading `v`, e.g. `0.4.0` or
  `1.2.0-rc.1`. There is no `latest` or other floating tag. OCI tags cannot
  contain `+`, so build metadata uses `_` (`1.2.0+build.1` is tagged
  `1.2.0_build.1`), as OpenTofu expects.
- Each tag holds the unchanged zips of the GitHub release, one per platform.

Point the provider at the mirror in the
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

The module keeps `source = "joelmuehlena/pdns"`. The `zh:` hash that
`tofu init` writes to `.terraform.lock.hcl` is the zip's SHA-256, the same
hash the OpenTofu Registry records, so lock files work with either source.

- **Pre-releases:** as with the registry, OpenTofu selects a pre-release only
  when the version constraint names it exactly (`version = "1.2.0-rc.1"`).
- **Credentials:** the public ghcr.io package needs none. For a private
  registry, OpenTofu uses the credentials of `docker login`, `podman login`
  or `oras login`; see
  [OCI Registry Credentials](https://opentofu.org/docs/cli/oci_registries/credentials/).
- **TLS:** OpenTofu talks to OCI registries over HTTPS only. For a registry
  with a private CA, add the CA to the system trust store or point
  `SSL_CERT_FILE` at it.

## Copying into your own registry

Copy a version with [ORAS](https://oras.land/) (>= 1.3.0) and use your
registry in `repository_template`. `-r` also copies the cosign signature and
the build provenance attestation, so both can be verified against the copy.

```sh
oras cp -r ghcr.io/joelmuehlena/terraform-provider-pdns:0.4.0 \
  registry.example.com/opentofu/terraform-provider-pdns:0.4.0
```

```hcl
    repository_template = "registry.example.com/opentofu/terraform-provider-${type}"
```

Without direct access from the target network, copy through an
[OCI image layout](https://github.com/opencontainers/image-spec/blob/main/image-layout.md)
directory:

```sh
# connected machine
oras cp -r ghcr.io/joelmuehlena/terraform-provider-pdns:0.4.0 \
  --to-oci-layout terraform-provider-pdns:0.4.0
oras resolve --oci-layout terraform-provider-pdns:0.4.0   # note the digest

# carry the terraform-provider-pdns directory across, then
oras cp -r --from-oci-layout terraform-provider-pdns:0.4.0 \
  registry.internal.example/opentofu/terraform-provider-pdns:0.4.0
```

## Verifying releases

Every release has a GPG signature of its checksum file, which `terraform init`
and `tofu init` check when installing from a registry. Releases after v0.3.0
also have:

- a cosign signature of the OCI artifact;
- a GitHub build provenance attestation of the OCI artifact and of every
  release asset.

The certificate identities below name the GitHub repository
`joelMuehlena/terraform-provider-pdns`. They are case-sensitive, while ghcr.io
paths are lowercase.

### Checksums and GPG signature

Each GitHub release has `terraform-provider-pdns_<version>_SHA256SUMS`, the
SHA-256 of every zip and of `terraform-provider-pdns_<version>_manifest.json`,
and its detached GPG signature `terraform-provider-pdns_<version>_SHA256SUMS.sig`.
The public keys are in
[`signing-keys.asc`](https://github.com/joelMuehlena/terraform-provider-pdns/blob/main/signing-keys.asc)
and registered with the Terraform and OpenTofu registries:

| Key | Fingerprint | Signs |
| --- | --- | --- |
| ed25519 `A57F07EF6DCBE584` | `504C C366 3F83 F71B 372E  FACD A57F 07EF 6DCB E584` | releases after v0.3.0 |
| rsa4096 `E4CF7FA9B79D791C` | `1A43 2CE0 2F50 5D0A 77BA  45CC E4CF 7FA9 B79D 791C` | v0.3.0 and earlier (retired, no longer used) |

```sh
gpg --import signing-keys.asc
gh release download v0.4.0 --repo joelMuehlena/terraform-provider-pdns \
  --pattern '*_SHA256SUMS*' --pattern '*_linux_amd64.zip'
gpg --verify terraform-provider-pdns_0.4.0_SHA256SUMS.sig terraform-provider-pdns_0.4.0_SHA256SUMS
sha256sum --ignore-missing -c terraform-provider-pdns_0.4.0_SHA256SUMS
```

The OCI artifact's layer digests are the same SHA-256 values. OpenTofu checks
a zip from an OCI mirror only against its digest and does not verify the GPG
or cosign signature, so verify the artifact before you mirror or install it.

### cosign signature

The release workflow signs the index of the OCI artifact by digest with
[cosign](https://docs.sigstore.dev/cosign/) keyless signing: the certificate
names the release workflow run for the tag, and the signature is recorded in
the Rekor transparency log. The index references every platform's zip by
digest, so verifying it covers all platforms. With cosign 3:

```sh
cosign verify \
  --certificate-identity "https://github.com/joelMuehlena/terraform-provider-pdns/.github/workflows/release.yml@refs/tags/v0.4.0" \
  --certificate-oidc-issuer https://token.actions.githubusercontent.com \
  ghcr.io/joelmuehlena/terraform-provider-pdns:0.4.0
```

The identity names the tag with its `v`. To accept any release tag, use
`--certificate-identity-regexp '^https://github\.com/joelMuehlena/terraform-provider-pdns/\.github/workflows/release\.yml@refs/tags/v'`
instead. cosign resolves the tag to a digest; to mirror exactly what you
verified, verify and copy by digest (`oras resolve ghcr.io/joelmuehlena/terraform-provider-pdns:0.4.0`).

### Build provenance

The release workflow creates a
[GitHub artifact attestation](https://docs.github.com/en/actions/security-guides/using-artifact-attestations-to-establish-provenance-for-builds)
(SLSA build provenance) for the index of the OCI artifact and for every
release asset listed in `_SHA256SUMS`. It records the repository, the
workflow, the tag and the commit the release was built from. Verify it with
the [GitHub CLI](https://cli.github.com/manual/gh_attestation_verify)
(`gh auth login` first; for registries other than the public ghcr.io package,
`gh` uses the credentials of `docker login`):

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

The attestation of the OCI artifact is stored with GitHub's attestations API,
where `gh` looks by default, and next to the artifact in the registry, where
`--bundle-from-oci` reads it.

### Air-gapped verification

Neither tool needs network access when it gets the signature or attestation
from the registry or a file and the Sigstore trusted root as a file. On a
connected machine, fetch the trusted roots and copy the artifact into an OCI
layout as in [Copying into your own registry](#copying-into-your-own-registry):

```sh
# trusted root for cosign (cached in ~/.sigstore/root, or in $TUF_ROOT if set)
cosign initialize
cp ~/.sigstore/root/tuf-repo-cdn.sigstore.dev/targets/trusted_root.json .
# trusted root for gh
gh attestation trusted-root > trusted_root.jsonl
```

Carry both files across with the layout, push it to the internal registry and
verify the digest there:

```sh
cosign verify \
  --trusted-root trusted_root.json \
  --certificate-identity "https://github.com/joelMuehlena/terraform-provider-pdns/.github/workflows/release.yml@refs/tags/v0.4.0" \
  --certificate-oidc-issuer https://token.actions.githubusercontent.com \
  registry.internal.example/opentofu/terraform-provider-pdns@sha256:<digest>
gh attestation verify oci://registry.internal.example/opentofu/terraform-provider-pdns@sha256:<digest> \
  --repo joelMuehlena/terraform-provider-pdns \
  --bundle-from-oci --custom-trusted-root trusted_root.jsonl
```

For a release zip, download its attestation on the connected machine with
`gh attestation download <zip> --repo joelMuehlena/terraform-provider-pdns`
(it writes `sha256:<digest>.jsonl`) and pass `--bundle sha256:<digest>.jsonl`
instead of `--bundle-from-oci`.

Without `--trusted-root` and `--custom-trusted-root`, both tools fetch the
trusted root from Sigstore's TUF repository, and without a bundle `gh` asks
the GitHub API; offline, both fail. Refresh the trusted roots from time to
time: they must contain the keys that were valid when a release was signed,
and Sigstore adds keys when it rotates them.
