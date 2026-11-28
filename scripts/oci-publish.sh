#!/usr/bin/env bash
# Publishes the provider's release archives as an OpenTofu OCI provider mirror
# artifact (https://opentofu.org/docs/cli/oci_registries/provider-mirror/).
#
#   scripts/oci-publish.sh DIR VERSION REPOSITORY [oras cp flags...]
#
#   DIR         directory with terraform-provider-pdns_<VERSION>_<os>_<arch>.zip,
#               e.g. goreleaser's dist/ or the assets of a GitHub release.
#               If it holds the _SHA256SUMS file, the archives are checked
#               against it first.
#   VERSION     provider version, a leading "v" is dropped (v1.2.0-rc.1 -> 1.2.0-rc.1)
#   REPOSITORY  target repository without tag, e.g. ghcr.io/joelmuehlena/terraform-provider-pdns
#   oras cp flags, e.g. --to-plain-http or --to-ca-file ca.pem for a local registry
#
# Prints the pushed artifact as REPOSITORY@sha256:... on stdout (progress goes
# to stderr), so callers can sign it by digest.
#
# The artifact is tagged with the exact version only (no "latest" or other
# floating tags); pre-releases such as 1.2.0-rc.1 get their exact tag as
# well. OCI tags cannot contain "+", so build metadata is written with "_"
# instead, which is how OpenTofu maps versions to tags.
#
# Layout: one image manifest per platform (artifactType
# application/vnd.opentofu.provider-target, platform os/arch, a single
# archive/zip layer that is the unchanged release zip) and an index manifest
# (artifactType application/vnd.opentofu.provider) over all of them. It is
# assembled in a temporary OCI layout and copied to REPOSITORY in one step,
# so the tag never points at a partial artifact.
#
# Environment:
#   OCI_SOURCE  value of the org.opencontainers.image.source annotation
#               (default https://github.com/joelMuehlena/terraform-provider-pdns)
#
# Needs oras >= 1.3.0 (see mise.toml). Log in to the registry first, e.g.
# `oras login ghcr.io`.
set -euo pipefail

if [ "$#" -lt 3 ]; then
  sed -n '2,/^set -euo/p' "$0" | sed '$d; s/^# \{0,1\}//' >&2
  exit 2
fi

dir=$1
version=${2#v}
repository=$3
shift 3

project=terraform-provider-pdns
source_url=${OCI_SOURCE:-https://github.com/joelMuehlena/terraform-provider-pdns}
tag=${version//+/_}

command -v oras >/dev/null || {
  echo "oras not found; run 'mise install' or see https://oras.land" >&2
  exit 1
}

checksums="${project}_${version}_SHA256SUMS"
if [ -f "$dir/$checksums" ]; then
  if command -v sha256sum >/dev/null; then sha256=(sha256sum); else sha256=(shasum -a 256); fi
  # Only the archives are published; the manifest JSON is not needed here.
  (cd "$dir" && grep '\.zip$' "$checksums" | "${sha256[@]}" -c -) >&2
fi

layout=$(mktemp -d)
trap 'rm -rf "$layout"' EXIT

platforms=()
for zip in "$dir/${project}_${version}"_*.zip; do
  [ -f "$zip" ] || {
    echo "no ${project}_${version}_<os>_<arch>.zip in $dir" >&2
    exit 1
  }
  name=$(basename "$zip")
  platform=${name#"${project}_${version}_"}
  platform=${platform%.zip} # e.g. linux_amd64
  os=${platform%%_*}
  arch=${platform#*_}
  # Run from DIR so the layer is titled with the plain file name.
  (cd "$dir" && oras push --no-tty \
    --artifact-type application/vnd.opentofu.provider-target \
    --artifact-platform "$os/$arch" \
    --oci-layout "$layout:$platform" \
    "$name:archive/zip" >/dev/null)
  platforms+=("$platform")
  echo "packed $os/$arch" >&2
done

oras manifest index create \
  --artifact-type application/vnd.opentofu.provider \
  --annotation "org.opencontainers.image.source=$source_url" \
  --annotation "org.opencontainers.image.version=$version" \
  --annotation "org.opencontainers.image.title=$project" \
  --annotation "org.opencontainers.image.description=PowerDNS provider for OpenTofu (OCI provider mirror)" \
  --annotation "org.opencontainers.image.licenses=Apache-2.0" \
  --oci-layout "$layout:$tag" "${platforms[@]}" >/dev/null

digest=$(oras manifest fetch --oci-layout --format go-template='{{.digest}}' "$layout:$tag")
oras cp --no-tty --from-oci-layout "$layout:$tag" "$repository:$tag" "$@" >&2
echo "$repository@$digest"
