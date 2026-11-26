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
				// Locked by default, before any key is attached.
				Config: config("", false),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("pdns_zone.z", "lock_dnsupdate", "true"),
					checkDNSUpdateList(fake, "example.test.", dnsupdateLockKey),
				),
			},
			{
				// Attached keys are listed next to the placeholder.
				Config: config("", true),
				Check:  checkDNSUpdateList(fake, "example.test.", dnsupdateLockKey, "tenant-a"),
			},
			{
				// Removing the last key keeps the zone locked.
				Config: config("", false),
				Check:  checkDNSUpdateList(fake, "example.test.", dnsupdateLockKey),
			},
			{
				// Placeholder removed out of band: drift, restored in place.
				PreConfig: func() {
					fake.mutate(func(f *fakePDNS) { delete(f.zones["example.test"].metadata, metadataTSIGAllowDNSUpdate) })
				},
				Config: config("", false),
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

func TestZoneResourceCreateUnlocked(t *testing.T) {
	t.Parallel()
	fake := newFakePDNS(t)

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: testProviderFactories,
		Steps: []resource.TestStep{{
			Config: zoneConfig(fake, `
  lock_dnsupdate = false
  nameservers    = [{ hostname = "ns1" }]
  soa            = { rname = "hostmaster" }`),
			Check: checkDNSUpdateList(fake, "example.test."),
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
  kind        = "`+kind+`"
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
		"secondary": {`kind = "Slave"`, []string{"Slave"}, []string{dnsupdateLockKey}},
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
  nameservers = [{ hostname = "ns1" }]
  soa         = { rname = "hostmaster" }`),
					ExpectError: regexp.MustCompile(`rejected by test`),
				}},
				// The half-created zone is removed again.
				CheckDestroy: checkZoneGone(fake, "example.test."),
			})
		})
	}
}
