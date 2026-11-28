# Read by the dynamic stack with terraform_remote_state.

output "zone" {
  description = "The zone the dynamic stack updates."
  value       = pdns_zone.example.name
}

output "dnsupdate_server" {
  description = "Server and port for RFC2136 updates, as seen from the host."
  value = {
    address = "127.0.0.1"
    port    = var.dns_port
  }
}

output "dnsupdate_key" {
  description = "The TSIG key of the dynamic stack."
  sensitive   = true
  value = {
    name      = pdns_tsig_key.dynamic.id
    algorithm = pdns_tsig_key.dynamic.algorithm
    secret    = pdns_tsig_key.dynamic.key
  }
}

# For `dig -y` (see README.md).
output "bind_axfr_key" {
  description = "The TSIG key BIND transfers example.test with, as hmac-sha256:name:secret."
  sensitive   = true
  value       = "${pdns_tsig_key.bind_axfr.algorithm}:${pdns_tsig_key.bind_axfr.name}:${pdns_tsig_key.bind_axfr.key}"
}
