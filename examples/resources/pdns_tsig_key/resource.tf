# Secret stored in state.
resource "pdns_tsig_key" "tenant_a" {
  name      = "tenant-a"
  algorithm = "hmac-sha256"
  key       = var.tenant_a_tsig_secret
}

# Secret passed write-only: never stored in plan or state.
# Requires Terraform >= 1.11 or OpenTofu >= 1.11.
variable "tenant_b_tsig_secret" {
  type      = string
  sensitive = true
  ephemeral = true
}

resource "pdns_tsig_key" "tenant_b" {
  name   = "tenant-b"
  key_wo = var.tenant_b_tsig_secret
  # Bump to roll out a new secret.
  key_wo_version = 1
}

# Generated secret, write-only: an ephemeral resource creates a fresh random
# value on every run, but it is only sent to PowerDNS on create or when
# `key_wo_version` changes. Pass the same ephemeral value to another
# write-only argument (e.g. of a secret store) to hand it to the tenant.
# Requires hashicorp/random >= 3.7.0.
ephemeral "random_password" "tenant_c" {
  length = 32
}

resource "pdns_tsig_key" "tenant_c" {
  name           = "tenant-c"
  key_wo         = base64encode(ephemeral.random_password.tenant_c.result)
  key_wo_version = 1
}

# Generated secret, stored in state: readable later via the `key` attribute.
resource "random_bytes" "tenant_d" {
  length = 32
}

resource "pdns_tsig_key" "tenant_d" {
  name = "tenant-d"
  key  = random_bytes.tenant_d.base64
}
