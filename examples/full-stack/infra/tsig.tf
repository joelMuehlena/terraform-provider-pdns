# TSIG keys and their attachments to the zones.
#
# The secrets are generated here and stored in state (`key`), because they
# are handed on: to BIND through bind/keys/tsig-keys.conf and to the dynamic
# stack through an output. A secret that only PowerDNS needs, or one that
# comes from a secret store, can use the write-only `key_wo` instead (never
# stored in state, Terraform/OpenTofu >= 1.11), see docs/resources/tsig_key.md.

# BIND transfers example.test from PowerDNS with this key.
resource "random_bytes" "bind_axfr" {
  length = 32
}

resource "pdns_tsig_key" "bind_axfr" {
  name      = "bind-axfr"
  algorithm = "hmac-sha256"
  key       = random_bytes.bind_axfr.base64
}

resource "pdns_zone_tsig_key" "example_bind_axfr" {
  zone        = pdns_zone.example.name
  tsig_key_id = pdns_tsig_key.bind_axfr.id
  axfr        = true
}

# PowerDNS transfers legacy.test from BIND with this key. A zone has only one
# secondary key, so this attachment must not use create_before_destroy.
resource "random_bytes" "pdns_secondary" {
  length = 32
}

resource "pdns_tsig_key" "pdns_secondary" {
  name      = "pdns-secondary"
  algorithm = "hmac-sha256"
  key       = random_bytes.pdns_secondary.base64
}

resource "pdns_zone_tsig_key" "legacy_secondary" {
  zone        = pdns_zone.legacy.name
  tsig_key_id = pdns_tsig_key.pdns_secondary.id
  secondary   = true
}

# The dynamic stack sends RFC2136 updates of example.test with this key.
resource "random_bytes" "dynamic" {
  length = 32
}

resource "pdns_tsig_key" "dynamic" {
  name      = "dynamic"
  algorithm = "hmac-sha256"
  key       = random_bytes.dynamic.base64
}

resource "pdns_zone_tsig_key" "example_dynamic" {
  zone        = pdns_zone.example.name
  tsig_key_id = pdns_tsig_key.dynamic.id
  dnsupdate   = true

  # When the attachment is replaced (another key or zone), add the new key
  # before removing the old one, so the zone always has a dnsupdate key.
  lifecycle {
    create_before_destroy = true
  }
}

# The keys of BIND, included by bind/named.conf. BIND reads the file at
# start and on `rndc reconfig`: restart it after a secret changed. The file
# must be readable by the bind user in the container, hence 0644; it is
# ignored by git.
resource "local_sensitive_file" "bind_keys" {
  filename        = "${path.module}/../bind/keys/tsig-keys.conf"
  file_permission = "0644"
  content         = <<-EOT
    // Written by infra/ (terraform apply). Do not edit.
    key "${pdns_tsig_key.bind_axfr.name}" {
      algorithm ${pdns_tsig_key.bind_axfr.algorithm};
      secret "${pdns_tsig_key.bind_axfr.key}";
    };
    key "${pdns_tsig_key.pdns_secondary.name}" {
      algorithm ${pdns_tsig_key.pdns_secondary.algorithm};
      secret "${pdns_tsig_key.pdns_secondary.key}";
    };
  EOT
}
