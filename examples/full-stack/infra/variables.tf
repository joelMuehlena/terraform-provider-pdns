variable "pdns_endpoint" {
  description = "URL of the PowerDNS HTTP API (PDNS_API_PORT in compose.yml)."
  type        = string
  default     = "http://127.0.0.1:18281"
}

variable "pdns_api_key" {
  description = "API key of PowerDNS (PDNS_API_KEY in compose.yml). The default is for the local example only."
  type        = string
  sensitive   = true
  default     = "example-api-key"
}

variable "dns_port" {
  description = "Host port of the PowerDNS DNS listener (AUTH_DNS_PORT in compose.yml), for the dynamic stack."
  type        = number
  default     = 15480
}

# Addresses in the compose network (compose.yml), used for the glue records
# and the primaries of the Slave zone.
variable "pdns_address" {
  description = "Address of pdns-auth in the compose network."
  type        = string
  default     = "10.53.0.10"
}

variable "bind_address" {
  description = "Address of BIND in the compose network."
  type        = string
  default     = "10.53.0.30"
}
