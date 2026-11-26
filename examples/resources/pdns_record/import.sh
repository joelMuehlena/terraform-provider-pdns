# Records are imported by `<zone>/<name>/<type>`. Give `name` exactly as in the
# configuration: relative to the zone (`test`) or fully qualified
# (`test.example.com.`). The records are imported in the order and form
# PowerDNS returns them (sorted, canonical), so if the configuration lists them
# differently, the first plan shows an in-place update that only rewrites them.
terraform import pdns_record.test2_a example.com./test2/A
