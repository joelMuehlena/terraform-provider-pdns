package provider

import (
	"context"
	"fmt"
	"regexp"
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

// TestAccZoneResourceTTL verifies that the SOA, NS and glue rrsets are written
// with the zone's ttl, on create and on updates, and that changing it updates
// them in place.
func TestAccZoneResourceTTL(t *testing.T) {
	env := testAccPreCheck(t)
	name := env.zoneName(t, "ttl")
	config := func(ttl, nameservers string) string {
		return accZoneConfig(env, name, ttl+`
  nameservers = [`+nameservers+`]
  soa         = { rname = "hostmaster" }`)
	}
	two := `{ hostname = "ns1", address = "10.0.0.1" }, { hostname = "ns2", address = "2001:db8::2" }`
	soa := "SOA " + name + " ns1." + name + " hostmaster." + name + " SERIAL 86400 7200 4000000 11200"
	update := resource.ConfigPlanChecks{
		PreApply: []plancheck.PlanCheck{plancheck.ExpectResourceAction("pdns_zone.z", plancheck.ResourceActionUpdate)},
	}

	resource.ParallelTest(t, resource.TestCase{
		ProtoV6ProviderFactories: testProviderFactories,
		CheckDestroy:             env.checkZoneGone(name),
		Steps: []resource.TestStep{
			{
				Config: config("", two),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("pdns_zone.z", "ttl", "3600"),
					env.checkRRsets(name, soa, "NS "+name+" ns1."+name+",ns2."+name, "A ns1."+name+" 10.0.0.1", "AAAA ns2."+name+" 2001:db8::2"),
				),
			},
			{
				Config:           config("ttl = 300", two),
				ConfigPlanChecks: update,
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("pdns_zone.z", "ttl", "300"),
					env.checkRRsetsTTL(name, 300, soa, "NS "+name+" ns1."+name+",ns2."+name, "A ns1."+name+" 10.0.0.1", "AAAA ns2."+name+" 2001:db8::2"),
				),
			},
			{
				// A nameserver change keeps the TTL.
				Config:           config("ttl = 300", `{ hostname = "ns1", address = "10.0.0.1" }, { hostname = "ns3", address = "10.0.0.3" }`),
				ConfigPlanChecks: update,
				Check:            env.checkRRsetsTTL(name, 300, soa, "NS "+name+" ns1."+name+",ns3."+name, "A ns1."+name+" 10.0.0.1", "A ns3."+name+" 10.0.0.3"),
			},
			{
				// The SOA TTL changed out of band shows as drift.
				PreConfig: func() {
					env.replaceRRset(t, name, name, "SOA", "ns1."+name+" hostmaster."+name+" 1 86400 7200 4000000 11200")
				},
				Config:           config("ttl = 300", `{ hostname = "ns1", address = "10.0.0.1" }, { hostname = "ns3", address = "10.0.0.3" }`),
				ConfigPlanChecks: update,
				Check:            env.checkRRsetsTTL(name, 300, soa, "NS "+name+" ns1."+name+",ns3."+name, "A ns1."+name+" 10.0.0.1", "A ns3."+name+" 10.0.0.3"),
			},
		},
	})
}

// checkStateSerial asserts that the serial in state is the one of the zone on
// the server, so the next refresh does not change it.
func (e *accEnv) checkStateSerial(zoneName string) func(*terraform.State) error {
	return func(s *terraform.State) error {
		zone, err := e.zone(zoneName)
		if err != nil {
			return err
		}
		return resource.TestCheckResourceAttr("pdns_zone.z", "serial", fmt.Sprint(zone.Serial))(s)
	}
}

func TestAccZoneResourceSerialMatchesServer(t *testing.T) {
	env := testAccPreCheck(t)

	tests := map[string]string{
		"without SOA record": `soa = { rname = "hostmaster", create_record = false }`,
		// The kind switch of the locked create changes the serial.
		"locked with DNSSEC": `
  soa            = { rname = "hostmaster" }
  lock_dnsupdate = true
  dnssec         = true`,
	}
	for name, body := range tests {
		t.Run(name, func(t *testing.T) {
			zone := env.zoneName(t, "serial")
			config := func(ns string) string {
				return accZoneConfig(env, zone, `
  nameservers = [{ hostname = "`+ns+`" }]
  `+body)
			}

			resource.ParallelTest(t, resource.TestCase{
				ProtoV6ProviderFactories: testProviderFactories,
				CheckDestroy:             env.checkZoneGone(zone),
				Steps: []resource.TestStep{
					{Config: config("ns1"), Check: env.checkStateSerial(zone)},
					// PowerDNS bumps the serial on updates (SOA-EDIT-API).
					{Config: config("ns2"), Check: env.checkStateSerial(zone)},
				},
			})
		})
	}
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

func (e *accEnv) checkMasters(zoneName, kind string, want ...string) func(*terraform.State) error {
	return func(*terraform.State) error {
		zone, err := e.zone(zoneName)
		if err != nil {
			return err
		}
		if zone.Kind != kind || strings.Join(zone.Masters, ",") != strings.Join(want, ",") {
			return fmt.Errorf("zone %s kind=%s masters=%v, want kind=%s masters=%v", zoneName, zone.Kind, zone.Masters, kind, want)
		}
		return nil
	}
}

func TestAccZoneResourceMasters(t *testing.T) {
	env := testAccPreCheck(t)
	name := env.zoneName(t, "masters")
	config := func(body string) string {
		return accZoneConfig(env, name, body+`
  nameservers = [{ hostname = "ns1" }]
  soa         = { rname = "hostmaster" }`)
	}
	update := resource.ConfigPlanChecks{
		PreApply: []plancheck.PlanCheck{plancheck.ExpectResourceAction("pdns_zone.z", plancheck.ResourceActionUpdate)},
	}

	resource.ParallelTest(t, resource.TestCase{
		ProtoV6ProviderFactories: testProviderFactories,
		CheckDestroy:             env.checkZoneGone(name),
		Steps: []resource.TestStep{
			{
				// PowerDNS drops port 53 and lowercases IPv6 addresses; that
				// is no drift.
				Config: config(`
  kind           = "Slave"
  lock_dnsupdate = true
  masters        = ["192.0.2.1:53", "2001:DB8::1", "[2001:db8::2]:5300"]`),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("pdns_zone.z", "masters.#", "3"),
					env.checkMasters(name, "Slave", "192.0.2.1", "2001:db8::1", "[2001:db8::2]:5300"),
				),
			},
			{
				Config:           config(`kind = "Slave"` + "\n" + `lock_dnsupdate = true` + "\n" + `masters = ["192.0.2.9"]`),
				ConfigPlanChecks: update,
				Check:            env.checkMasters(name, "Slave", "192.0.2.9"),
			},
			{
				// No longer a secondary: the primaries are removed.
				Config:           config(`lock_dnsupdate = true`),
				ConfigPlanChecks: update,
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckNoResourceAttr("pdns_zone.z", "masters"),
					env.checkMasters(name, "Native"),
				),
			},
		},
	})
}

func TestAccZoneResourceNameserverAddressOutOfZone(t *testing.T) {
	env := testAccPreCheck(t)
	name := env.zoneName(t, "ooz")

	resource.ParallelTest(t, resource.TestCase{
		ProtoV6ProviderFactories: testProviderFactories,
		CheckDestroy:             env.checkZoneGone(name),
		Steps: []resource.TestStep{
			{
				// PowerDNS would refuse the glue record with 422 "Name is out
				// of zone"; the plan fails instead.
				Config: accZoneConfig(env, name, `
  nameservers = [{ hostname = "ns1.other.org.", address = "10.0.0.1" }]
  soa         = { rname = "hostmaster" }`),
				PlanOnly:    true,
				ExpectError: regexp.MustCompile(`Nameserver address outside the zone`),
			},
			{
				// Nameservers outside the zone without address are fine.
				Config: accZoneConfig(env, name, `
  nameservers = [
    { hostname = "ns1", address = "10.0.0.1" },
    { hostname = "ns2.other.org." },
  ]
  soa = { rname = "hostmaster" }`),
				Check: env.checkRRsets(name,
					"SOA "+name+" ns1."+name+" hostmaster."+name+" SERIAL 86400 7200 4000000 11200",
					"NS "+name+" ns1."+name+",ns2.other.org.",
					"A ns1."+name+" 10.0.0.1",
				),
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
