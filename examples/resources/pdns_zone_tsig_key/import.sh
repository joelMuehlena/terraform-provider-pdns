# Attachments are imported by `<zone>:<tsig_key_id>` (":" because "/" is valid
# in RFC 2317 classless reverse zones such as `0/26.2.0.192.in-addr.arpa.`).
# A missing trailing dot is added to both, like PowerDNS ids have it.
terraform import pdns_zone_tsig_key.tenant_a_example_com example.com.:tenant-a.
