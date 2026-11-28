package provider

import (
	"fmt"
	"regexp"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
)

// accServerConfig returns a zone and a TSIG key managed through a provider
// with the given endpoint and server_id.
func accServerConfig(env *accEnv, endpoint, serverID, zone, key string) string {
	return fmt.Sprintf(`
provider "pdns" {
  endpoint  = %q
  api_key   = %q
  server_id = %q
}

resource "pdns_zone" "z" {
  name        = %q
  nameservers = [{ hostname = "ns1", addresses = ["10.0.0.1"] }]
  soa         = { rname = "hostmaster" }
}

resource "pdns_tsig_key" "k" {
  name = %q
  key  = %q
}
`, endpoint, env.apiKey, serverID, zone, key, secretA)
}

// TestAccProviderWrongServer checks that a wrong server_id or a wrong API path
// in the endpoint is an error. PowerDNS answers them with the same
// 404 {"error":"Not Found"} as an unknown zone or key, which used to empty the
// state on refresh and make destroy succeed without deleting anything.
func TestAccProviderWrongServer(t *testing.T) {
	env := testAccPreCheck(t)
	zone := env.zoneName(t, "server")
	key := env.keyName(t, "server")

	resource.ParallelTest(t, resource.TestCase{
		ProtoV6ProviderFactories: testProviderFactories,
		CheckDestroy: resource.ComposeAggregateTestCheckFunc(
			env.checkZoneGone(zone),
			env.checkKeyGone(key),
		),
		Steps: []resource.TestStep{
			{
				// A trailing slash on the endpoint is ignored (it used to make
				// every request go to //api/v1/..., which PowerDNS answers 404).
				Config: accServerConfig(env, env.endpoint+"/", "localhost", zone, key),
				Check: resource.ComposeAggregateTestCheckFunc(
					env.checkKey(key, "hmac-sha256", secretA),
					resource.TestCheckResourceAttr("pdns_zone.z", "name", zone),
				),
			},
			{
				Config:      accServerConfig(env, env.endpoint, "wrong", zone, key),
				ExpectError: regexp.MustCompile(`server_id "wrong"\s+not\s+found`),
			},
			{
				// The endpoint must be the base URL, without /api/v1.
				Config:      accServerConfig(env, env.endpoint+"/api/v1", "localhost", zone, key),
				ExpectError: regexp.MustCompile(`server_id "localhost"\s+not\s+found`),
			},
			{
				// Nothing was dropped from the state or deleted on the server.
				Config:   accServerConfig(env, env.endpoint, "localhost", zone, key),
				PlanOnly: true,
				Check:    env.checkKey(key, "hmac-sha256", secretA),
			},
		},
	})
}
