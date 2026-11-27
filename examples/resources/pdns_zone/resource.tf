resource "pdns_zone" "example_com" {
  name = "example.com."

  dnssec = false

  nameservers = [
    {
      # Dual-stack glue: an A and an AAAA record at ns1.example.com.
      hostname  = "ns1",
      addresses = ["10.10.10.1", "2001:db8::1"]
    },
    {
      hostname  = "ns2",
      addresses = ["10.10.10.2"]
    },
    {
      # Outside the zone, so without glue records.
      hostname = "ns.example.net."
    }
  ]

  soa = {
    rname = "hostmaster"

    refresh = 10800
    retry   = 3600
    expire  = 604800
    ttl     = 3600
  }

}

