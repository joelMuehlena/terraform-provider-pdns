resource "pdns_tsig_key" "tenant_a" {
  name = "tenant-a"
  key  = var.tenant_a_tsig_secret
}

resource "pdns_tsig_key" "tenant_b" {
  name = "tenant-b"
  key  = var.tenant_b_tsig_secret
}

# One key attached to several zones: tenant-a may send dynamic updates to
# both zones and transfer (AXFR) example.com.
resource "pdns_zone_tsig_key" "tenant_a_example_com" {
  zone        = "example.com."
  tsig_key_id = pdns_tsig_key.tenant_a.id
  dnsupdate   = true
  axfr        = true

  # On replacement (another zone or key), add the new key before removing the
  # old one, so that TSIG-ALLOW-DNSUPDATE is never empty in between.
  lifecycle {
    create_before_destroy = true
  }
}

resource "pdns_zone_tsig_key" "tenant_a_example_org" {
  zone        = "example.org."
  tsig_key_id = pdns_tsig_key.tenant_a.id
  dnsupdate   = true

  lifecycle {
    create_before_destroy = true
  }
}

# Several keys on one zone.
resource "pdns_zone_tsig_key" "tenant_b_example_com" {
  zone        = "example.com."
  tsig_key_id = pdns_tsig_key.tenant_b.id
  dnsupdate   = true

  lifecycle {
    create_before_destroy = true
  }
}
