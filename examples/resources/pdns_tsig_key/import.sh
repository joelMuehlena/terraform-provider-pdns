# TSIG keys are imported by their PowerDNS id (the name with a trailing dot).
terraform import pdns_tsig_key.tenant_a tenant-a.

# A key managed with `key_wo` gets its secret stored in `key` on import, until
# the next apply re-sends `key_wo`: see the note on importing above.
