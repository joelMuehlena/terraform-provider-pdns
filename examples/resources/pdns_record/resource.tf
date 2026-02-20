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

resource "pdns_record" "test_aaaa" {
  zone = "example.com."

  name = "test"

  type = "AAAA"
  records = [
    "2345:0425:2CA1:0000:0000:0567:5673:23b5",
  ]
}

resource "pdns_record" "test2_a" {
  zone = pdns_zone.example_com.name

  name = "test2"

  type = "A"
  records = [
    "10.10.10.4",
  ]
}

