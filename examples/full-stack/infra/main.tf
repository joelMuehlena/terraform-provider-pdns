# Stack 1: the PowerDNS zones, records and TSIG keys. Its outputs give the
# dnsupdate key to stack 2 (../dynamic), which has its own state.

terraform {
  required_version = ">= 1.6"

  required_providers {
    pdns = {
      source = "joelmuehlena/pdns"
      # pdns_tsig_key, pdns_zone_tsig_key, pdns_zone ttl and addresses are
      # not in v0.3.0.
      version = ">= 0.4.0, < 1.0.0"
    }
    random = {
      source  = "hashicorp/random"
      version = "~> 3.6"
    }
    local = {
      source  = "hashicorp/local"
      version = "~> 2.5"
    }
  }
}

provider "pdns" {
  endpoint = var.pdns_endpoint
  api_key  = var.pdns_api_key
}

# Primary zone in PowerDNS. Kind Master: PowerDNS sends NOTIFY to BIND, which
# transfers it as secondary (see tsig.tf for the key).
resource "pdns_zone" "example" {
  name = "example.test."
  kind = "Master"
  ttl  = 300

  nameservers = [
    { hostname = "ns1", addresses = [var.pdns_address] },
    { hostname = "ns2", addresses = [var.bind_address] },
  ]

  soa = {
    rname   = "hostmaster"
    refresh = 300
    retry   = 60
    expire  = 86400
    ttl     = 60
  }
}

resource "pdns_record" "www" {
  zone    = pdns_zone.example.name
  name    = "www"
  type    = "A"
  ttl     = 300
  records = ["192.0.2.10"]
}

resource "pdns_record" "info" {
  zone    = pdns_zone.example.name
  name    = "info"
  type    = "TXT"
  ttl     = 300
  records = ["\"managed by the infra stack\""]
}

# Secondary zone in PowerDNS, transferred from BIND (bind/zones/legacy.test.zone).
# The zone content comes from BIND: the provider does not write the SOA
# record (create_record = false), and nameservers and ttl repeat the NS and
# glue records of the zone file, so that the plan stays empty after the
# transfer.
resource "pdns_zone" "legacy" {
  name    = "legacy.test."
  kind    = "Slave"
  masters = ["${var.bind_address}:5353"]

  nameservers = [
    { hostname = "ns", addresses = [var.bind_address] },
  ]

  soa = {
    rname         = "hostmaster"
    create_record = false
  }
}
