package provider

import (
	"fmt"
	"net/http"
	"regexp"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/plancheck"
	"github.com/hashicorp/terraform-plugin-testing/terraform"
	"github.com/samber/lo"
	"gitlab.com/joelMuehlena/homelab/code/terraform/provider/terraform-provider-pdns/internal/pdns_client"
)

func zoneConfig(fake *fakePDNS, body string) string {
	return fake.providerConfig() + `
resource "pdns_zone" "z" {
  name = "example.test."
` + body + `
}
`
}

// checkRRsets asserts the rrsets of a zone (excluding NS) on the fake server
// as "TYPE name content..." lines. The SOA serial is replaced by SERIAL.
func checkRRsets(fake *fakePDNS, zoneName string, want ...string) resource.TestCheckFunc {
	serialRe := regexp.MustCompile(`^(\S+ \S+ )\d+ `)
	return func(*terraform.State) error {
		zone, ok := fake.zone(zoneName)
		if !ok {
			return fmt.Errorf("zone %s does not exist", zoneName)
		}
		var got []string
		for _, rr := range zone.rrsets {
			if rr.Type == "NS" {
				continue
			}
			contents := lo.Map(rr.Records, func(r pdns_client.Record, _ int) string {
				if rr.Type == "SOA" {
					return serialRe.ReplaceAllString(r.Content, "${1}SERIAL ")
				}
				return r.Content
			})
			got = append(got, rr.Type+" "+rr.Name+" "+strings.Join(contents, ","))
		}
		slices.Sort(got)
		slices.Sort(want)
		if !slices.Equal(got, want) {
			return fmt.Errorf("rrsets =\n  %s\nwant\n  %s", strings.Join(got, "\n  "), strings.Join(want, "\n  "))
		}
		return nil
	}
}

func checkZoneGone(fake *fakePDNS, name string) resource.TestCheckFunc {
	return func(*terraform.State) error {
		if _, ok := fake.zone(name); ok {
			return fmt.Errorf("zone %s still exists", name)
		}
		return nil
	}
}

func TestZoneResource(t *testing.T) {
	t.Parallel()
	fake := newFakePDNS(t)
	today := time.Now().Format("20060102")

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: testProviderFactories,
		CheckDestroy:             checkZoneGone(fake, "example.test."),
		Steps: []resource.TestStep{
			{
				Config: zoneConfig(fake, `
  nameservers = [
    { hostname = "ns1", address = "10.0.0.1" },
    { hostname = "ns2", address = "2001:db8::2" },
  ]
  soa = { rname = "hostmaster" }`),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("pdns_zone.z", "kind", "Native"),
					resource.TestCheckResourceAttr("pdns_zone.z", "dnssec", "false"),
					resource.TestCheckResourceAttr("pdns_zone.z", "serial", today+"01"),
					resource.TestCheckResourceAttr("pdns_zone.z", "soa.refresh", "86400"),
					resource.TestCheckResourceAttr("pdns_zone.z", "soa.create_record", "true"),
					checkRRsets(fake, "example.test.",
						"SOA example.test. ns1.example.test. hostmaster.example.test. SERIAL 86400 7200 4000000 11200",
						"A ns1.example.test. 10.0.0.1",
						"AAAA ns2.example.test. 2001:db8::2",
					),
				),
			},
			{
				// Nameserver and SOA changes: records follow, serial increases.
				Config: zoneConfig(fake, `
  nameservers = [
    { hostname = "ns1", address = "10.0.0.9" },
    { hostname = "ns3", address = "10.0.0.3" },
  ]
  soa = {
    rname   = "admin"
    refresh = 3600
  }`),
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{plancheck.ExpectResourceAction("pdns_zone.z", plancheck.ResourceActionUpdate)},
				},
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("pdns_zone.z", "serial", today+"02"),
					checkRRsets(fake, "example.test.",
						"SOA example.test. ns1.example.test. admin.example.test. SERIAL 3600 7200 4000000 11200",
						"A ns1.example.test. 10.0.0.9",
						"A ns3.example.test. 10.0.0.3",
					),
					func(*terraform.State) error {
						zone, _ := fake.zone("example.test.")
						ns, _ := zone.rrset("example.test.", "NS")
						got := lo.Map(ns.Records, func(r pdns_client.Record, _ int) string { return r.Content })
						if !slices.Equal(got, []string{"ns1.example.test.", "ns3.example.test."}) {
							return fmt.Errorf("NS = %v", got)
						}
						return nil
					},
				),
			},
			{
				// Kind and DNSSEC are zone settings, not records.
				Config: zoneConfig(fake, `
  kind   = "Master"
  dnssec = true
  nameservers = [
    { hostname = "ns1", address = "10.0.0.9" },
    { hostname = "ns3", address = "10.0.0.3" },
  ]
  soa = {
    rname   = "admin"
    refresh = 3600
  }`),
				Check: func(*terraform.State) error {
					zone, _ := fake.zone("example.test.")
					if zone.kind != "Master" || !zone.dnssec {
						return fmt.Errorf("zone kind=%s dnssec=%v", zone.kind, zone.dnssec)
					}
					return nil
				},
			},
			{
				// And back: dnssec = false must be sent explicitly.
				Config: zoneConfig(fake, `
  nameservers = [
    { hostname = "ns1", address = "10.0.0.9" },
    { hostname = "ns3", address = "10.0.0.3" },
  ]
  soa = {
    rname   = "admin"
    refresh = 3600
  }`),
				Check: func(*terraform.State) error {
					zone, _ := fake.zone("example.test.")
					if zone.kind != "Native" || zone.dnssec {
						return fmt.Errorf("zone kind=%s dnssec=%v", zone.kind, zone.dnssec)
					}
					return nil
				},
			},
		},
	})
}

func TestZoneResourceWithoutSOARecord(t *testing.T) {
	t.Parallel()
	fake := newFakePDNS(t)

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: testProviderFactories,
		Steps: []resource.TestStep{{
			Config: zoneConfig(fake, `
  nameservers = [{ hostname = "ns1.other.org." }]
  soa = {
    rname         = "hostmaster"
    create_record = false
  }`),
			// The default SOA record of the server is not read back into soa.
			Check: checkRRsets(fake, "example.test.",
				"SOA example.test. a.misconfigured.dns.server.invalid. hostmaster.example.test. SERIAL 10800 3600 604800 3600",
			),
		}},
	})
}

func TestZoneResourceFullyQualifiedNames(t *testing.T) {
	t.Parallel()
	fake := newFakePDNS(t)
	today := time.Now().Format("20060102")
	config := zoneConfig(fake, `
  nameservers = [{ hostname = "ns1.example.test.", address = "10.0.0.1" }]
  soa         = { rname = "hostmaster.example.test." }`)

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: testProviderFactories,
		Steps: []resource.TestStep{
			{
				// Fully qualified names inside the zone are read back with
				// their configured spelling, so there is no diff after apply.
				Config: config,
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("pdns_zone.z", "soa.rname", "hostmaster.example.test."),
					resource.TestCheckResourceAttr("pdns_zone.z", "nameservers.0.hostname", "ns1.example.test."),
				),
			},
			{
				// Re-applying does not rewrite the SOA (the serial stays).
				Config: config,
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{plancheck.ExpectEmptyPlan()},
				},
				Check: resource.TestCheckResourceAttr("pdns_zone.z", "serial", today+"01"),
			},
			{
				// A changed rname still shows as drift and is corrected.
				PreConfig: func() {
					fake.mutate(func(f *fakePDNS) {
						z := f.zones["example.test"]
						for i, rr := range z.rrsets {
							if rr.Type == "SOA" {
								z.rrsets[i].Records = []pdns_client.Record{{Content: "ns1.example.test. admin.example.test. " + today + "01 86400 7200 4000000 11200"}}
							}
						}
					})
				},
				Config: config,
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{plancheck.ExpectResourceAction("pdns_zone.z", plancheck.ResourceActionUpdate)},
				},
				Check: checkRRsets(fake, "example.test.",
					"SOA example.test. ns1.example.test. hostmaster.example.test. SERIAL 86400 7200 4000000 11200",
					"A ns1.example.test. 10.0.0.1",
				),
			},
		},
	})
}

// setGlue replaces the A rrset at name in the fake zone; no contents removes it.
func setGlue(fake *fakePDNS, zoneName, name string, contents ...string) {
	fake.mutate(func(f *fakePDNS) {
		z := f.zones[fakeKey(zoneName)]
		z.rrsets = slices.DeleteFunc(z.rrsets, func(rr pdns_client.Rrset) bool { return rr.Name == name && rr.Type == "A" })
		if len(contents) > 0 {
			z.rrsets = append(z.rrsets, pdns_client.Rrset{Name: name, Type: "A", Records: lo.Map(contents, func(c string, _ int) pdns_client.Record {
				return pdns_client.Record{Content: c}
			})})
		}
	})
}

func TestZoneResourceNameserverAddressDrift(t *testing.T) {
	t.Parallel()
	fake := newFakePDNS(t)
	config := zoneConfig(fake, `
  nameservers = [
    { hostname = "ns1", address = "10.0.0.1" },
    { hostname = "ns2", address = "10.0.0.2" },
  ]
  soa = { rname = "hostmaster" }`)
	want := checkRRsets(fake, "example.test.",
		"SOA example.test. ns1.example.test. hostmaster.example.test. SERIAL 86400 7200 4000000 11200",
		"A ns1.example.test. 10.0.0.1",
		"A ns2.example.test. 10.0.0.2",
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

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: testProviderFactories,
		Steps: []resource.TestStep{
			{Config: config, Check: want},
			// Changed, removed or extended out of band: refresh reports the
			// drift and the apply restores the glue record.
			driftStep(func() { setGlue(fake, "example.test.", "ns2.example.test.", "10.0.0.9") }),
			driftStep(func() { setGlue(fake, "example.test.", "ns2.example.test.") }),
			driftStep(func() { setGlue(fake, "example.test.", "ns1.example.test.", "10.0.0.1", "10.0.0.8") }),
		},
	})
}

// importStep imports pdns_zone.z by its name and verifies the imported
// state against the state of the previous step.
func importStep(config string) resource.TestStep {
	return resource.TestStep{
		Config:            config,
		ResourceName:      "pdns_zone.z",
		ImportState:       true,
		ImportStateId:     "example.test.",
		ImportStateVerify: true,
		// The import ID is the zone name; there is no id attribute.
		ImportStateVerifyIdentifierAttribute: "name",
	}
}

func TestZoneResourceImport(t *testing.T) {
	t.Parallel()

	configs := map[string]string{
		"managed SOA": `
  kind           = "Master"
  lock_dnsupdate = true
  nameservers = [
    { hostname = "ns1", address = "10.0.0.1" },
    # Import lists the SOA primary first, then the others sorted by name.
    { hostname = "ns.other.org." },
    { hostname = "ns2", address = "2001:db8::2" },
  ]
  soa = {
    rname   = "hostmaster"
    refresh = 3600
    retry   = 600
    expire  = 86400
    ttl     = 300
  }`,
		"out of zone rname": `
  nameservers = [{ hostname = "ns1" }]
  soa         = { rname = "admin.other.org." }`,
		// The default SOA record of PowerDNS imports as create_record = false.
		"without SOA record": `
  nameservers = [{ hostname = "ns1.other.org." }]
  soa = {
    rname         = "hostmaster"
    create_record = false
  }`,
	}
	for name, body := range configs {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			fake := newFakePDNS(t)
			config := zoneConfig(fake, body)
			resource.UnitTest(t, resource.TestCase{
				ProtoV6ProviderFactories: testProviderFactories,
				// Refresh first: without a managed SOA record, Create stores
				// no serial, and the refresh stores the server's one.
				Steps: []resource.TestStep{{Config: config}, {RefreshState: true}, importStep(config)},
			})
		})
	}
}

func TestZoneResourceImportExisting(t *testing.T) {
	t.Parallel()
	fake := newFakePDNS(t)
	fake.mutate(func(f *fakePDNS) {
		f.zones["example.test"] = &fakeZone{name: "example.test.", kind: "Native", serial: 2024010101, metadata: map[string][]string{}, rrsets: []pdns_client.Rrset{
			{Name: "example.test.", Type: "SOA", Records: []pdns_client.Record{{Content: "ns1.example.test. hostmaster.example.test. 2024010101 3600 600 86400 300"}}},
			{Name: "example.test.", Type: "NS", Records: []pdns_client.Record{{Content: "ns1.example.test."}, {Content: "ns.other.org."}}},
			{Name: "ns1.example.test.", Type: "A", Records: []pdns_client.Record{{Content: "10.0.0.1"}}},
			{Name: "www.example.test.", Type: "A", Records: []pdns_client.Record{{Content: "10.0.0.80"}}},
		}}
	})
	config := zoneConfig(fake, `
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

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: testProviderFactories,
		Steps: []resource.TestStep{
			{
				Config:             config,
				ResourceName:       "pdns_zone.z",
				ImportState:        true,
				ImportStateId:      "example.test.",
				ImportStatePersist: true,
				ImportStateCheck: func(states []*terraform.InstanceState) error {
					want := map[string]string{
						"soa.rname": "hostmaster", "soa.refresh": "3600", "soa.ttl": "300", "soa.create_record": "true",
						"nameservers.#": "2", "nameservers.0.hostname": "ns1", "nameservers.0.address": "10.0.0.1",
						"nameservers.1.hostname": "ns.other.org.", "serial": "2024010101", "lock_dnsupdate": "false",
					}
					for k, v := range want {
						if got := states[0].Attributes[k]; got != v {
							return fmt.Errorf("%s = %q, want %q", k, got, v)
						}
					}
					if _, ok := states[0].Attributes["nameservers.1.address"]; ok {
						return fmt.Errorf("nameservers.1.address = %q, want unset", states[0].Attributes["nameservers.1.address"])
					}
					return nil
				},
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

func TestZoneResourceErrors(t *testing.T) {
	t.Parallel()

	t.Run("already exists", func(t *testing.T) {
		t.Parallel()
		fake := newFakePDNS(t)
		fake.addZone("example.test.")

		resource.UnitTest(t, resource.TestCase{
			ProtoV6ProviderFactories: testProviderFactories,
			Steps: []resource.TestStep{{
				Config:      zoneConfig(fake, `nameservers = [{ hostname = "ns1" }]`+"\n"+`soa = { rname = "hostmaster" }`),
				ExpectError: regexp.MustCompile(`Zone already exists`),
			}},
		})
	})

	validation := map[string]struct{ body, wantErr string }{
		"name without dot":  {`nameservers = [{ hostname = "ns1" }]` + "\n" + `soa = { rname = "h" }`, `Name must end with a dot`},
		"no nameservers":    {`nameservers = []` + "\n" + `soa = { rname = "h" }`, `at least 1`},
		"invalid address":   {`nameservers = [{ hostname = "ns1", address = "nope" }]` + "\n" + `soa = { rname = "h" }`, `not a valid IPv4 or\s+valid IPv6`},
		"invalid kind":      {`kind = "Bogus"` + "\n" + `nameservers = [{ hostname = "ns1" }]` + "\n" + `soa = { rname = "h" }`, `Invalid Attribute Value Match`},
		"missing soa":       {`nameservers = [{ hostname = "ns1" }]`, `"soa" is required`},
		"missing soa rname": {`nameservers = [{ hostname = "ns1" }]` + "\n" + `soa = {}`, `rname`},
	}
	for name, tt := range validation {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			fake := newFakePDNS(t)
			config := zoneConfig(fake, tt.body)
			if name == "name without dot" {
				config = strings.Replace(config, `"example.test."`, `"example.test"`, 1)
			}
			resource.UnitTest(t, resource.TestCase{
				ProtoV6ProviderFactories: testProviderFactories,
				Steps: []resource.TestStep{{
					Config:      config,
					PlanOnly:    true,
					ExpectError: regexp.MustCompile(tt.wantErr),
				}},
			})
		})
	}
}

// checkDNSUpdateList asserts the zone's TSIG-ALLOW-DNSUPDATE on the fake
// server, ignoring order and spelling.
func checkDNSUpdateList(fake *fakePDNS, zoneName string, want ...string) resource.TestCheckFunc {
	return func(*terraform.State) error {
		zone, ok := fake.zone(zoneName)
		if !ok {
			return fmt.Errorf("zone %s does not exist", zoneName)
		}
		if got, want := normalizedNames(zone.metadata[metadataTSIGAllowDNSUpdate]), normalizedNames(want); !slices.Equal(got, want) {
			return fmt.Errorf("zone %s dnsupdate list = %v, want %v", zoneName, got, want)
		}
		return nil
	}
}

func TestZoneResourceDNSUpdateLock(t *testing.T) {
	t.Parallel()
	fake := newFakePDNS(t)

	config := func(lock string, attached bool) string {
		c := zoneConfig(fake, lock+`
  nameservers = [{ hostname = "ns1" }]
  soa         = { rname = "hostmaster" }`) + `
resource "pdns_tsig_key" "a" {
  name = "tenant-a"
  key  = "` + secretA + `"
}
`
		if attached {
			c += `
resource "pdns_zone_tsig_key" "a" {
  zone        = pdns_zone.z.name
  tsig_key_id = pdns_tsig_key.a.id
  dnsupdate   = true
}
`
		}
		return c
	}

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: testProviderFactories,
		Steps: []resource.TestStep{
			{
				// Locked from creation, before any key is attached.
				Config: config("lock_dnsupdate = true", false),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("pdns_zone.z", "lock_dnsupdate", "true"),
					checkDNSUpdateList(fake, "example.test.", dnsupdateLockKey),
				),
			},
			{
				// Attached keys are listed next to the placeholder.
				Config: config("lock_dnsupdate = true", true),
				Check:  checkDNSUpdateList(fake, "example.test.", dnsupdateLockKey, "tenant-a"),
			},
			{
				// Removing the last key keeps the zone locked.
				Config: config("lock_dnsupdate = true", false),
				Check:  checkDNSUpdateList(fake, "example.test.", dnsupdateLockKey),
			},
			{
				// Placeholder removed out of band: drift, restored in place.
				PreConfig: func() {
					fake.mutate(func(f *fakePDNS) { delete(f.zones["example.test"].metadata, metadataTSIGAllowDNSUpdate) })
				},
				Config: config("lock_dnsupdate = true", false),
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{plancheck.ExpectResourceAction("pdns_zone.z", plancheck.ResourceActionUpdate)},
				},
				Check: checkDNSUpdateList(fake, "example.test.", dnsupdateLockKey),
			},
			{
				// Opting out removes only the placeholder.
				Config: config("lock_dnsupdate = false", true),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("pdns_zone.z", "lock_dnsupdate", "false"),
					checkDNSUpdateList(fake, "example.test.", "tenant-a"),
				),
			},
			{
				Config: config("lock_dnsupdate = false", false),
				Check:  checkDNSUpdateList(fake, "example.test."),
			},
			{
				// Locking again.
				Config: config("lock_dnsupdate = true", true),
				Check:  checkDNSUpdateList(fake, "example.test.", dnsupdateLockKey, "tenant-a"),
			},
		},
	})
}

func TestZoneResourceLockDNSUpdateDefaultsToFalse(t *testing.T) {
	t.Parallel()
	fake := newFakePDNS(t)

	// Servers with dnsupdate-require-tsig do not need the lock, so it is opt-in.
	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: testProviderFactories,
		Steps: []resource.TestStep{{
			Config: zoneConfig(fake, `
  nameservers = [{ hostname = "ns1" }]
  soa         = { rname = "hostmaster" }`),
			Check: resource.ComposeAggregateTestCheckFunc(
				resource.TestCheckResourceAttr("pdns_zone.z", "lock_dnsupdate", "false"),
				checkDNSUpdateList(fake, "example.test."),
			),
		}},
	})
}

// assertNeverUnlocked fails the test if the zone is ever stored with a kind
// that applies dynamic updates while TSIG-ALLOW-DNSUPDATE is empty, and
// records the kinds the zone went through.
func assertNeverUnlocked(t *testing.T, fake *fakePDNS, zoneName string) *[]string {
	t.Helper()
	var kinds []string
	fake.afterWrite = func(f *fakePDNS, r *http.Request) {
		z, ok := f.zones[fakeKey(zoneName)]
		if !ok {
			return
		}
		if len(kinds) == 0 || kinds[len(kinds)-1] != z.kind {
			kinds = append(kinds, z.kind)
		}
		if !isSecondaryKind(z.kind) && len(z.metadata[metadataTSIGAllowDNSUpdate]) == 0 {
			t.Errorf("after %s %s the zone is %s without dnsupdate lock", r.Method, r.URL.Path, z.kind)
		}
	}
	return &kinds
}

func TestZoneResourceCreateIsNeverUnlocked(t *testing.T) {
	t.Parallel()

	for _, kind := range []string{"Native", "Master", "Producer"} {
		t.Run(kind, func(t *testing.T) {
			t.Parallel()
			fake := newFakePDNS(t)
			kinds := assertNeverUnlocked(t, fake, "example.test.")

			resource.UnitTest(t, resource.TestCase{
				ProtoV6ProviderFactories: testProviderFactories,
				Steps: []resource.TestStep{{
					Config: zoneConfig(fake, `
  kind           = "`+kind+`"
  lock_dnsupdate = true
  nameservers = [{ hostname = "ns1", address = "10.0.0.1" }]
  soa         = { rname = "hostmaster" }`),
					Check: resource.ComposeAggregateTestCheckFunc(
						resource.TestCheckResourceAttr("pdns_zone.z", "kind", kind),
						checkDNSUpdateList(fake, "example.test.", dnsupdateLockKey),
						checkRRsets(fake, "example.test.",
							"SOA example.test. ns1.example.test. hostmaster.example.test. SERIAL 86400 7200 4000000 11200",
							"A ns1.example.test. 10.0.0.1",
						),
						func(*terraform.State) error {
							if want := []string{"Slave", kind}; !slices.Equal(*kinds, want) {
								return fmt.Errorf("zone kinds = %v, want %v", *kinds, want)
							}
							return nil
						},
					),
				}},
			})
		})
	}
}

func TestZoneResourceCreateDirect(t *testing.T) {
	t.Parallel()

	tests := map[string]struct {
		body      string
		wantKinds []string
		wantLock  []string
	}{
		// Not locked: no detour through Slave.
		"unlocked": {`lock_dnsupdate = false`, []string{"Native"}, nil},
		// Secondary zones never apply updates: created directly, then locked.
		"secondary": {"kind = \"Slave\"\nlock_dnsupdate = true", []string{"Slave"}, []string{dnsupdateLockKey}},
	}
	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			fake := newFakePDNS(t)
			var kinds []string
			fake.afterWrite = func(f *fakePDNS, _ *http.Request) {
				if z, ok := f.zones["example.test"]; ok && (len(kinds) == 0 || kinds[len(kinds)-1] != z.kind) {
					kinds = append(kinds, z.kind)
				}
			}

			resource.UnitTest(t, resource.TestCase{
				ProtoV6ProviderFactories: testProviderFactories,
				Steps: []resource.TestStep{{
					Config: zoneConfig(fake, tt.body+`
  nameservers = [{ hostname = "ns1" }]
  soa         = { rname = "hostmaster" }`),
					Check: resource.ComposeAggregateTestCheckFunc(
						checkDNSUpdateList(fake, "example.test.", tt.wantLock...),
						func(*terraform.State) error {
							if !slices.Equal(kinds, tt.wantKinds) {
								return fmt.Errorf("zone kinds = %v, want %v", kinds, tt.wantKinds)
							}
							return nil
						},
					),
				}},
			})
		})
	}
}

func TestZoneResourceCreateRollsBack(t *testing.T) {
	t.Parallel()

	steps := map[string]func(r *http.Request) bool{
		"lock fails": func(r *http.Request) bool {
			return r.Method == http.MethodPut && strings.Contains(r.URL.Path, "/metadata/")
		},
		"kind switch fails": func(r *http.Request) bool {
			return r.Method == http.MethodPut && strings.HasSuffix(r.URL.Path, "/zones/example.test.")
		},
	}
	for name, reject := range steps {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			fake := newFakePDNS(t)
			fake.reject = reject

			resource.UnitTest(t, resource.TestCase{
				ProtoV6ProviderFactories: testProviderFactories,
				Steps: []resource.TestStep{{
					Config: zoneConfig(fake, `
  lock_dnsupdate = true
  nameservers    = [{ hostname = "ns1" }]
  soa            = { rname = "hostmaster" }`),
					ExpectError: regexp.MustCompile(`rejected by test`),
				}},
				// The half-created zone is removed again.
				CheckDestroy: checkZoneGone(fake, "example.test."),
			})
		})
	}
}

func TestZoneResourceDeletedOutOfBand(t *testing.T) {
	t.Parallel()
	fake := newFakePDNS(t)
	config := zoneConfig(fake, `
  nameservers = [{ hostname = "ns1" }]
  soa         = { rname = "hostmaster" }`) + `
resource "pdns_record" "www" {
  zone    = pdns_zone.z.name
  name    = "www"
  type    = "A"
  records = ["10.0.0.1"]
}
`
	deleteZone := func() { fake.mutate(func(f *fakePDNS) { delete(f.zones, "example.test") }) }

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: testProviderFactories,
		Steps: []resource.TestStep{
			{Config: config},
			{
				// Refresh removes zone and record from state; apply re-creates both.
				PreConfig: deleteZone,
				Config:    config,
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{
						plancheck.ExpectResourceAction("pdns_zone.z", plancheck.ResourceActionCreate),
						plancheck.ExpectResourceAction("pdns_record.www", plancheck.ResourceActionCreate),
					},
				},
				Check: checkRRsets(fake, "example.test.",
					"SOA example.test. ns1.example.test. hostmaster.example.test. SERIAL 86400 7200 4000000 11200",
					"A www.example.test. 10.0.0.1",
				),
			},
			{
				// Destroy succeeds although the zone is already gone.
				PreConfig: deleteZone,
				Config:    fake.providerConfig(),
				Destroy:   true,
			},
		},
	})
}
