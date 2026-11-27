package provider

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/plancheck"
	"github.com/hashicorp/terraform-plugin-testing/terraform"
	"github.com/samber/lo"
	"gitlab.com/joelMuehlena/homelab/code/terraform/provider/terraform-provider-pdns/internal/pdns_client"
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

func TestAccZoneResourceCreateWithDNSSEC(t *testing.T) {
	env := testAccPreCheck(t)

	tests := map[string]string{
		"unlocked": `lock_dnsupdate = false`,
		// Created as Slave, locked, then switched to Native.
		"locked": `lock_dnsupdate = true`,
		// Without a SOA record PowerDNS cannot rectify the zone when DNSSEC is
		// only enabled with the kind switch.
		"locked without SOA record": `
  lock_dnsupdate = true
  soa            = { rname = "hostmaster", create_record = false }`,
	}
	for name, body := range tests {
		t.Run(name, func(t *testing.T) {
			zone := env.zoneName(t, "dnssec")
			if !strings.Contains(body, "soa") {
				body += "\n  soa = { rname = \"hostmaster\" }"
			}

			resource.ParallelTest(t, resource.TestCase{
				ProtoV6ProviderFactories: testProviderFactories,
				CheckDestroy:             env.checkZoneGone(zone),
				Steps: []resource.TestStep{{
					Config: accZoneConfig(env, zone, `
  dnssec      = true
  nameservers = [{ hostname = "ns1" }]
  `+body),
					Check: resource.ComposeAggregateTestCheckFunc(
						resource.TestCheckResourceAttr("pdns_zone.z", "dnssec", "true"),
						env.checkZoneSettings(zone, "Native", true),
					),
				}},
			})
		})
	}
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

// replaceRRset replaces (or with no contents deletes) an rrset of a zone on
// the server, as a change made outside of Terraform.
func (e *accEnv) replaceRRset(t *testing.T, zoneName, name, typ string, contents ...string) {
	t.Helper()
	rrset := pdns_client.Rrset{Name: name, Type: typ, Changetype: "DELETE"}
	if len(contents) > 0 {
		rrset.Changetype = "REPLACE"
		rrset.TTL = 3600
		rrset.Records = lo.Map(contents, func(c string, _ int) pdns_client.Record { return pdns_client.Record{Content: c} })
	}
	if err := e.client.UpdateZoneRecords(context.Background(), zoneName, []pdns_client.Rrset{rrset}); err != nil {
		t.Fatalf("change %s %s in %s: %v", typ, name, zoneName, err)
	}
}

func accImportStep(config, name string) resource.TestStep {
	return resource.TestStep{
		Config:                               config,
		ResourceName:                         "pdns_zone.z",
		ImportState:                          true,
		ImportStateId:                        name,
		ImportStateVerify:                    true,
		ImportStateVerifyIdentifierAttribute: "name",
	}
}

func TestAccZoneResourceImport(t *testing.T) {
	env := testAccPreCheck(t)

	bodies := map[string]string{
		"import": `
  kind           = "Master"
  lock_dnsupdate = true
  nameservers = [
    { hostname = "ns1", address = "10.0.0.1" },
    { hostname = "ns2", address = "2001:db8::2" },
    { hostname = "ns3.other.org." },
  ]
  soa = {
    rname   = "hostmaster"
    refresh = 3600
    retry   = 600
    expire  = 86400
    ttl     = 300
  }`,
		// The default SOA record of PowerDNS imports as create_record = false.
		"import-nosoa": `
  nameservers = [{ hostname = "ns1.other.org." }]
  soa = {
    rname         = "hostmaster"
    create_record = false
  }`,
	}
	for label, body := range bodies {
		t.Run(label, func(t *testing.T) {
			name := env.zoneName(t, label)
			config := accZoneConfig(env, name, body)
			resource.ParallelTest(t, resource.TestCase{
				ProtoV6ProviderFactories: testProviderFactories,
				CheckDestroy:             env.checkZoneGone(name),
				Steps: []resource.TestStep{
					{Config: config},
					// PowerDNS sets the serial (SOA-EDIT-API); the refresh stores it.
					{RefreshState: true},
					accImportStep(config, name),
				},
			})
		})
	}
}

func TestAccZoneResourceImportExisting(t *testing.T) {
	env := testAccPreCheck(t)
	name := env.zoneName(t, "import-existing")
	_, err := env.client.CreateZone(context.Background(), pdns_client.PDNSZone{
		Name:        name,
		Kind:        "Native",
		Nameservers: []string{"ns1." + name, "ns.other.org."},
		Rrsets: []pdns_client.Rrset{
			{Name: name, Type: "SOA", TTL: 3600, Records: []pdns_client.Record{{Content: "ns1." + name + " hostmaster." + name + " 2024010101 3600 600 86400 300"}}},
			{Name: "ns1." + name, Type: "A", TTL: 3600, Records: []pdns_client.Record{{Content: "10.0.0.1"}}},
		},
	})
	if err != nil {
		t.Fatalf("create zone %s: %v", name, err)
	}
	config := accZoneConfig(env, name, `
  nameservers = [
    { hostname = "ns1", address = "10.0.0.1" },
    { hostname = "ns.other.org." },
  ]
  soa = {
    rname   = "hostmaster"
    refresh = 3600
    retry   = 600
    expire  = 86400
    ttl     = 300
  }`)

	resource.ParallelTest(t, resource.TestCase{
		ProtoV6ProviderFactories: testProviderFactories,
		CheckDestroy:             env.checkZoneGone(name),
		Steps: []resource.TestStep{
			{
				Config:             config,
				ResourceName:       "pdns_zone.z",
				ImportState:        true,
				ImportStateId:      name,
				ImportStatePersist: true,
			},
			{
				// The imported zone matches the configuration.
				Config: config,
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{plancheck.ExpectEmptyPlan()},
				},
			},
		},
	})
}

func TestAccZoneResourceDrift(t *testing.T) {
	env := testAccPreCheck(t)
	name := env.zoneName(t, "drift")
	var serial int64

	// Fully qualified names inside the zone.
	config := accZoneConfig(env, name, `
  nameservers = [
    { hostname = "ns1.`+name+`", address = "10.0.0.1" },
    { hostname = "ns2", address = "10.0.0.2" },
  ]
  soa = { rname = "hostmaster.`+name+`" }`)
	want := env.checkRRsets(name,
		"SOA "+name+" ns1."+name+" hostmaster."+name+" SERIAL 86400 7200 4000000 11200",
		"NS "+name+" ns1."+name+",ns2."+name,
		"A ns1."+name+" 10.0.0.1",
		"A ns2."+name+" 10.0.0.2",
	)
	driftStep := func(mutate func()) resource.TestStep {
		return resource.TestStep{
			PreConfig: mutate,
			Config:    config,
			ConfigPlanChecks: resource.ConfigPlanChecks{
				PreApply: []plancheck.PlanCheck{plancheck.ExpectResourceAction("pdns_zone.z", plancheck.ResourceActionUpdate)},
			},
			Check: want,
		}
	}

	resource.ParallelTest(t, resource.TestCase{
		ProtoV6ProviderFactories: testProviderFactories,
		CheckDestroy:             env.checkZoneGone(name),
		Steps: []resource.TestStep{
			{Config: config, Check: resource.ComposeAggregateTestCheckFunc(want, env.recordSerial(name, &serial))},
			{
				// No diff from the spelling: the SOA is not rewritten.
				Config: config,
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{plancheck.ExpectEmptyPlan()},
				},
				Check: func(*terraform.State) error {
					zone, err := env.zone(name)
					if err != nil {
						return err
					}
					if zone.Serial != serial {
						return fmt.Errorf("serial changed from %d to %d", serial, zone.Serial)
					}
					return nil
				},
			},
			// Glue records changed, removed or extended out of band.
			driftStep(func() { env.replaceRRset(t, name, "ns2."+name, "A", "10.0.0.9") }),
			driftStep(func() { env.replaceRRset(t, name, "ns2."+name, "A") }),
			driftStep(func() { env.replaceRRset(t, name, "ns1."+name, "A", "10.0.0.1", "10.0.0.8") }),
			// The rname changed out of band.
			driftStep(func() {
				env.replaceRRset(t, name, name, "SOA", "ns1."+name+" admin."+name+" 1 86400 7200 4000000 11200")
			}),
		},
	})
}
