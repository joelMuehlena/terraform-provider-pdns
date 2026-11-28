# Registry image of the OCI dry run (the oci job in .github/workflows/ci.yml),
# read from this FROM line by the job. Not built: the file only pins the
# version and digest so that Dependabot (docker ecosystem) proposes updates.
FROM docker.io/library/registry:3.1.2@sha256:82f6ff4a7f982960c8aa51617fea3aabc485df6e55cce80d22deb1de36f1bc3c
