# Stack 2: records of example.test written with RFC2136 dynamic updates
# (hashicorp/dns), not through the PowerDNS API. It has its own state and
# only gets the zone and the TSIG key from the infra stack.

terraform {
  required_version = ">= 1.6"

  required_providers {
    dns = {
      source  = "hashicorp/dns"
      version = "~> 3.4"
    }
  }
}

# Reads the outputs of ../infra from its local state file. This stack can then
# only be planned by someone who can read that state, and it stores the
# outputs, including the TSIG secret, in its own state too. To keep the
# secret out of this state, pass it as a variable (e.g. TF_VAR_...) from a
# secret store instead.
data "terraform_remote_state" "infra" {
  backend = "local"

  config = {
    path = "${path.module}/../infra/terraform.tfstate"
  }
}

locals {
  infra = data.terraform_remote_state.infra.outputs
}

provider "dns" {
  update {
    server        = local.infra.dnsupdate_server.address
    port          = local.infra.dnsupdate_server.port
    key_name      = local.infra.dnsupdate_key.name
    key_algorithm = local.infra.dnsupdate_key.algorithm
    key_secret    = local.infra.dnsupdate_key.secret
  }
}

resource "dns_a_record_set" "app" {
  zone      = local.infra.zone
  name      = "app"
  addresses = ["192.0.2.100", "192.0.2.101"]
  ttl       = 60
}

resource "dns_txt_record_set" "app" {
  zone = local.infra.zone
  name = "app"
  txt  = ["managed by the dynamic stack"]
  ttl  = 60
}
