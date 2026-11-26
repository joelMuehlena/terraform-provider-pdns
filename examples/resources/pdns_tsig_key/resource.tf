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
