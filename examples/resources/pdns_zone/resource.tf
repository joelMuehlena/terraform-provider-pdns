resource "pdns_zone" "example_com" {
  name = "example.com."

  dnssec = false

  nameservers = [
    {
      hostname = "ns1",
      address  = "10.10.10.1"
    },
    {
      hostname = "ns2",
      address  = "10.10.10.2"
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

