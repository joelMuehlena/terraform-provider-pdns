package provider

import (
	"fmt"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/plancheck"
	"github.com/hashicorp/terraform-plugin-testing/terraform"
)

func accZoneConfig(env *accEnv, name, body string) string {
	return env.providerConfig() + fmt.Sprintf(`
resource "pdns_zone" "z" {
  name = %q
%s
}
`, name, body)
}

// recordSerial stores the current serial of a zone on the server in *serial.
func (e *accEnv) recordSerial(zoneName string, serial *int64) func(*terraform.State) error {
	return func(*terraform.State) error {
		zone, err := e.zone(zoneName)
		if err != nil {
			return err
		}
		*serial = zone.Serial
		return nil
	}
}

// checkSerialIncreased asserts that the serial of a zone on the server is
// above *prev and stores it in *prev. PowerDNS itself bumps the serial on
// every change (SOA-EDIT-API), so exact values are not asserted.
func (e *accEnv) checkSerialIncreased(zoneName string, prev *int64) func(*terraform.State) error {
	return func(*terraform.State) error {
		zone, err := e.zone(zoneName)
		if err != nil {
			return err
		}
		if zone.Serial <= *prev {
			return fmt.Errorf("serial of %s = %d, want > %d", zoneName, zone.Serial, *prev)
		}
		*prev = zone.Serial
		return nil
	}
}

func (e *accEnv) checkZoneSettings(zoneName, kind string, dnssec bool) func(*terraform.State) error {
	return func(*terraform.State) error {
		zone, err := e.zone(zoneName)
		if err != nil {
			return err
		}
		if zone.Kind != kind || zone.Dnssec != dnssec {
			return fmt.Errorf("zone %s kind=%s dnssec=%t, want kind=%s dnssec=%t", zoneName, zone.Kind, zone.Dnssec, kind, dnssec)
		}
		return nil
	}
}

func TestAccZoneResource(t *testing.T) {
	env := testAccPreCheck(t)
	name := env.zoneName(t, "zone")
	var serial int64

	nameservers2 := `
  nameservers = [
    { hostname = "ns1", address = "10.0.0.9" },
    { hostname = "ns3", address = "10.0.0.3" },
  ]
  soa = {
    rname   = "admin"
    refresh = 3600
  }`

	resource.ParallelTest(t, resource.TestCase{
		ProtoV6ProviderFactories: testProviderFactories,
		CheckDestroy:             env.checkZoneGone(name),
		Steps: []resource.TestStep{
			{
				Config: accZoneConfig(env, name, `
  nameservers = [
    { hostname = "ns1", address = "10.0.0.1" },
    { hostname = "ns2", address = "2001:db8::2" },
  ]
  soa = { rname = "hostmaster" }`),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("pdns_zone.z", "kind", "Native"),
					resource.TestCheckResourceAttr("pdns_zone.z", "dnssec", "false"),
					resource.TestCheckResourceAttrSet("pdns_zone.z", "serial"),
					env.checkRRsets(name,
						"SOA "+name+" ns1."+name+" hostmaster."+name+" SERIAL 86400 7200 4000000 11200",
						"NS "+name+" ns1."+name+",ns2."+name,
						"A ns1."+name+" 10.0.0.1",
						"AAAA ns2."+name+" 2001:db8::2",
					),
					env.checkZoneSettings(name, "Native", false),
					env.recordSerial(name, &serial),
				),
			},
			{
				// Nameserver and SOA changes: records follow, serial increases.
				Config: accZoneConfig(env, name, nameservers2),
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{plancheck.ExpectResourceAction("pdns_zone.z", plancheck.ResourceActionUpdate)},
				},
				Check: resource.ComposeAggregateTestCheckFunc(
					env.checkRRsets(name,
						"SOA "+name+" ns1."+name+" admin."+name+" SERIAL 3600 7200 4000000 11200",
						"NS "+name+" ns1."+name+",ns3."+name,
						"A ns1."+name+" 10.0.0.9",
						"A ns3."+name+" 10.0.0.3",
					),
					env.checkSerialIncreased(name, &serial),
				),
			},
			{
				// Only kind and DNSSEC change: no record rewrite, the serial in
				// state stays known (regression test for #67).
				Config: accZoneConfig(env, name, `
  kind   = "Master"
  dnssec = true
`+nameservers2),
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{plancheck.ExpectResourceAction("pdns_zone.z", plancheck.ResourceActionUpdate)},
				},
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttrSet("pdns_zone.z", "serial"),
					resource.TestCheckResourceAttr("pdns_zone.z", "kind", "Master"),
					resource.TestCheckResourceAttr("pdns_zone.z", "dnssec", "true"),
					env.checkZoneSettings(name, "Master", true),
				),
			},
			{
				// And back.
				Config: accZoneConfig(env, name, nameservers2),
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{plancheck.ExpectResourceAction("pdns_zone.z", plancheck.ResourceActionUpdate)},
				},
				Check: env.checkZoneSettings(name, "Native", false),
			},
		},
	})
}

func TestAccZoneResourceWithoutSOARecord(t *testing.T) {
	env := testAccPreCheck(t)
	name := env.zoneName(t, "nosoa")

	resource.ParallelTest(t, resource.TestCase{
		ProtoV6ProviderFactories: testProviderFactories,
		CheckDestroy:             env.checkZoneGone(name),
		Steps: []resource.TestStep{{
			Config: accZoneConfig(env, name, `
  nameservers = [{ hostname = "ns1.other.org." }]
  soa = {
    rname         = "hostmaster"
    create_record = false
  }`),
			Check: resource.ComposeAggregateTestCheckFunc(
				resource.TestCheckResourceAttr("pdns_zone.z", "soa.create_record", "false"),
				env.checkZoneSettings(name, "Native", false),
			),
		}},
	})
}
