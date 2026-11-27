# Zones are imported by their name (with the trailing dot).
#
# soa and nameservers are read from the zone's records:
# - soa from the SOA record. create_record is true, unless the zone has the
#   default SOA record PowerDNS creates for zones without one; then it is
#   false, rname is read from it and the other soa values are their defaults.
# - nameservers from the NS records: first the primary nameserver of the SOA
#   record, then the others sorted by name (the order PowerDNS returns them in
#   depends on the backend). Hostnames inside the zone are relative to it ("ns1"), others
#   fully qualified. A hostname inside the zone gets addresses from the A and
#   AAAA records at that name (IPv4 first, each sorted), also for a single
#   record: glue is always imported as addresses, never as the deprecated
#   address.
#
# Write names inside the zone relative to it, list the nameservers in the same
# order and set the addresses of every nameserver that has glue records, so
# the first plan after the import shows no changes. Otherwise that plan updates
# the zone to the configured values. A configuration that still sets address
# shows such an update (from addresses to address); it changes no glue record,
# but switch to addresses = ["<address>"] instead.
terraform import pdns_zone.example_com example.com.
