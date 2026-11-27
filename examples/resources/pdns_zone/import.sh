# Zones are imported by their name (with the trailing dot).
#
# soa and nameservers are read from the zone's records:
# - soa from the SOA record. create_record is true, unless the zone has the
#   default SOA record PowerDNS creates for zones without one; then it is
#   false, rname is read from it and the other soa values are their defaults.
# - nameservers from the NS records: first the primary nameserver of the SOA
#   record, then the others sorted by name (the order PowerDNS returns them in
#   depends on the backend). Hostnames inside the zone are relative to it ("ns1"), others
#   fully qualified. A hostname inside the zone gets its address from the A
#   record at that name, or else the AAAA record.
#
# Write names inside the zone relative to it, list the nameservers in the same
# order and set the address of every nameserver that has one, so the first
# plan after the import shows no changes. Otherwise that plan updates the zone
# to the configured values.
terraform import pdns_zone.example_com example.com.
