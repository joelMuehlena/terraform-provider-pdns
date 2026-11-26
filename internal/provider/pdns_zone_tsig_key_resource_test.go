package provider

import (
	"context"
	"fmt"
	"net/http"
	"regexp"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/plancheck"
	"github.com/hashicorp/terraform-plugin-testing/terraform"
	"gitlab.com/joelMuehlena/homelab/code/terraform/provider/terraform-provider-pdns/internal/pdns_client"
)

const metadataDNSUpdate = metadataTSIGAllowDNSUpdate

// zoneTSIGConfig attaches tenant-a to z1 (dnsupdate, axfr as given) and z2
// (dnsupdate), and tenant-b to z1 (dnsupdate, secondary).
func zoneTSIGConfig(fake *fakePDNS, aZ1AXFR bool) string {
	return fake.providerConfig() + fmt.Sprintf(`
resource "pdns_tsig_key" "a" {
  name = "tenant-a"
  key  = %[1]q
}

resource "pdns_tsig_key" "b" {
  name = "tenant-b"
  key  = %[2]q
}

resource "pdns_zone_tsig_key" "a_z1" {
  zone        = "z1.test."
  tsig_key_id = pdns_tsig_key.a.id
  dnsupdate   = true
  axfr        = %[3]t
}

resource "pdns_zone_tsig_key" "a_z2" {
  zone        = "z2.test."
  tsig_key_id = pdns_tsig_key.a.id
  dnsupdate   = true
}

resource "pdns_zone_tsig_key" "b_z1" {
  zone        = "z1.test."
  tsig_key_id = pdns_tsig_key.b.id
  dnsupdate   = true
  secondary   = true
}
`, secretA, secretB, aZ1AXFR)
}

// checkZoneLists asserts the TSIG lists of a zone on the fake server,
// ignoring order and spelling of the entries.
func checkZoneLists(fake *fakePDNS, zoneName string, dnsupdate, master, slave []string) resource.TestCheckFunc {
	return func(*terraform.State) error {
		zone, ok := fake.zone(zoneName)
		if !ok {
			return fmt.Errorf("zone %s does not exist", zoneName)
		}
		for name, pair := range map[string][2][]string{
			"dnsupdate": {zone.metadata[metadataDNSUpdate], dnsupdate},
			"master":    {zone.master, master},
			"slave":     {zone.slave, slave},
		} {
			if got, want := normalizedNames(pair[0]), normalizedNames(pair[1]); !slices.Equal(got, want) {
				return fmt.Errorf("zone %s %s list = %v, want %v", zoneName, name, got, want)
			}
		}
		return nil
	}
}

func normalizedNames(names []string) []string {
	out := make([]string, 0, len(names))
	for _, n := range names {
		out = append(out, fakeKey(n))
	}
	slices.Sort(out)
	return out
}

func TestZoneTSIGKeyResource(t *testing.T) {
	t.Parallel()
	fake := newFakePDNS(t)
	fake.addZone("z1.test.")
	fake.addZone("z2.test.")

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: testProviderFactories,
		CheckDestroy: resource.ComposeAggregateTestCheckFunc(
			checkZoneLists(fake, "z1.test.", nil, nil, nil),
			checkZoneLists(fake, "z2.test.", nil, nil, nil),
		),
		Steps: []resource.TestStep{
			{
				// Many keys per zone and many zones per key in one apply.
				Config: zoneTSIGConfig(fake, true),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("pdns_zone_tsig_key.a_z1", "id", "z1.test./tenant-a."),
					resource.TestCheckResourceAttr("pdns_zone_tsig_key.a_z1", "axfr", "true"),
					resource.TestCheckResourceAttr("pdns_zone_tsig_key.a_z1", "secondary", "false"),
					resource.TestCheckResourceAttr("pdns_zone_tsig_key.a_z2", "axfr", "false"),
					checkZoneLists(fake, "z1.test.", []string{"tenant-a", "tenant-b"}, []string{"tenant-a."}, []string{"tenant-b."}),
					checkZoneLists(fake, "z2.test.", []string{"tenant-a"}, nil, nil),
				),
			},
			{
				// Toggling a purpose updates in place and leaves the others alone.
				Config: zoneTSIGConfig(fake, false),
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{plancheck.ExpectResourceAction("pdns_zone_tsig_key.a_z1", plancheck.ResourceActionUpdate)},
				},
				Check: checkZoneLists(fake, "z1.test.", []string{"tenant-a", "tenant-b"}, nil, []string{"tenant-b."}),
			},
			{
				ResourceName:      "pdns_zone_tsig_key.b_z1",
				ImportState:       true,
				ImportStateId:     "z1.test./tenant-b.",
				ImportStateVerify: true,
			},
		},
	})
}

func TestZoneTSIGKeyResourceDrift(t *testing.T) {
	t.Parallel()
	fake := newFakePDNS(t)
	fake.addZone("z1.test.")
	fake.addZone("z2.test.")
	config := zoneTSIGConfig(fake, true)

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: testProviderFactories,
		Steps: []resource.TestStep{
			{Config: config},
			{
				// One purpose removed out of band: drift on that flag only.
				PreConfig: func() {
					fake.mutate(func(f *fakePDNS) { f.zones["z1.test"].metadata[metadataDNSUpdate] = []string{"tenant-a"} })
				},
				Config: config,
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{
						plancheck.ExpectResourceAction("pdns_zone_tsig_key.b_z1", plancheck.ResourceActionUpdate),
						plancheck.ExpectResourceAction("pdns_zone_tsig_key.a_z1", plancheck.ResourceActionNoop),
					},
				},
				Check: checkZoneLists(fake, "z1.test.", []string{"tenant-a", "tenant-b"}, []string{"tenant-a."}, []string{"tenant-b."}),
			},
			{
				// Every entry removed out of band: re-attached.
				PreConfig: func() {
					fake.mutate(func(f *fakePDNS) { delete(f.zones["z2.test"].metadata, metadataDNSUpdate) })
				},
				Config: config,
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{plancheck.ExpectResourceAction("pdns_zone_tsig_key.a_z2", plancheck.ResourceActionCreate)},
				},
				Check: checkZoneLists(fake, "z2.test.", []string{"tenant-a"}, nil, nil),
			},
			{
				// Zone deleted out of band: attachments dropped, destroy succeeds.
				PreConfig: func() {
					fake.mutate(func(f *fakePDNS) { delete(f.zones, "z2.test") })
				},
				Config:             config,
				PlanOnly:           true,
				ExpectNonEmptyPlan: true,
			},
			{
				PreConfig: func() { fake.addZone("z2.test.") },
				Config:    fake.providerConfig(),
				Destroy:   true,
			},
		},
	})
}

func TestZoneTSIGKeyResourceValidation(t *testing.T) {
	t.Parallel()
	fake := newFakePDNS(t)
	fake.addZone("z1.test.")
	fake.addKey("tenant-a", secretA)

	attachment := func(body string) string {
		return fake.providerConfig() + `
resource "pdns_zone_tsig_key" "x" {
  zone        = "z1.test."
  tsig_key_id = "tenant-a."
` + body + `
}
`
	}

	tests := map[string]struct {
		config  string
		wantErr string
	}{
		"no purpose":         {attachment(``), `No purpose enabled`},
		"all purposes false": {attachment("dnsupdate = false\naxfr = false\nsecondary = false"), `No purpose enabled`},
		"zone without dot":   {strings.Replace(attachment(`axfr = true`), `"z1.test."`, `"z1.test"`, 1), `Name must end with a dot`},
		"unknown key":        {strings.Replace(attachment(`axfr = true`), `"tenant-a."`, `"missing."`, 1), `TSIG key not found`},
		"unknown zone":       {strings.Replace(attachment(`axfr = true`), `"z1.test."`, `"missing.test."`, 1), `Zone not found`},
	}

	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			resource.UnitTest(t, resource.TestCase{
				ProtoV6ProviderFactories: testProviderFactories,
				Steps: []resource.TestStep{{
					Config:      tt.config,
					ExpectError: regexp.MustCompile(tt.wantErr),
				}},
			})
		})
	}
}

func TestZoneTSIGKeyResourceInvalidImportID(t *testing.T) {
	t.Parallel()
	fake := newFakePDNS(t)

	for _, id := range []string{"no-slash", "/tenant-a.", "z1.test./"} {
		t.Run(id, func(t *testing.T) {
			t.Parallel()
			resource.UnitTest(t, resource.TestCase{
				ProtoV6ProviderFactories: testProviderFactories,
				Steps: []resource.TestStep{{
					Config: fake.providerConfig() + `
resource "pdns_zone_tsig_key" "x" {
  zone        = "z1.test."
  tsig_key_id = "tenant-a."
  axfr        = true
}
`,
					ResourceName:  "pdns_zone_tsig_key.x",
					ImportState:   true,
					ImportStateId: id,
					ExpectError:   regexp.MustCompile(`Invalid import ID`),
				}},
			})
		})
	}
}

// newTestAttachment returns an attachment resource with its own provider
// data, i.e. its own zone locks, like a separate Terraform run would have.
func newTestAttachment(fake *fakePDNS) *ZoneTSIGKeyResource {
	return &ZoneTSIGKeyResource{providerData: &PDNSProviderData{
		pdnsClient: pdns_client.NewPDNSClient(fake.srv.Client(), fake.srv.URL, "localhost", fakeAPIKey),
	}}
}

func TestZoneTSIGKeyApplyConcurrentWriters(t *testing.T) {
	t.Parallel()
	fake := newFakePDNS(t)
	fake.addZone("race.test.")

	const writers = 20
	for i := range writers {
		fake.addKey(fmt.Sprintf("k%d", i), secretA)
	}

	// Writers in separate "runs" share no lock; only the retry protects them.
	run := func(want tsigPurposes) {
		var wg sync.WaitGroup
		errs := make(chan error, writers)
		for i := range writers {
			wg.Go(func() {
				name := fmt.Sprintf("k%d", i)
				errs <- newTestAttachment(fake).apply(context.Background(), "race.test.", name+".", name, want)
			})
		}
		wg.Wait()
		close(errs)
		for err := range errs {
			if err != nil {
				t.Fatal(err)
			}
		}
	}

	run(tsigPurposes{DNSUpdate: true, AXFR: true})
	zone, _ := fake.zone("race.test.")
	if len(zone.metadata[metadataDNSUpdate]) != writers || len(zone.master) != writers {
		t.Fatalf("after concurrent add: dnsupdate=%d axfr=%d, want %d each", len(zone.metadata[metadataDNSUpdate]), len(zone.master), writers)
	}

	run(tsigPurposes{})
	zone, _ = fake.zone("race.test.")
	if len(zone.metadata[metadataDNSUpdate]) != 0 || len(zone.master) != 0 {
		t.Fatalf("after concurrent remove: dnsupdate=%v axfr=%v, want none", zone.metadata[metadataDNSUpdate], zone.master)
	}
}

func TestZoneTSIGKeyApplyRetriesAfterInterference(t *testing.T) {
	t.Parallel()
	fake := newFakePDNS(t)
	fake.addZone("z.test.")
	fake.addKey("k", secretA)
	fake.addKey("other", secretB)

	// Another writer overwrites the list right after our first write.
	interfered := false
	fake.afterWrite = func(f *fakePDNS, r *http.Request) {
		if !interfered && r.Method == http.MethodPut {
			interfered = true
			f.zones["z.test"].metadata[metadataDNSUpdate] = []string{"other"}
		}
	}

	if err := newTestAttachment(fake).apply(context.Background(), "z.test.", "k.", "k", tsigPurposes{DNSUpdate: true}); err != nil {
		t.Fatal(err)
	}
	zone, _ := fake.zone("z.test.")
	if got := normalizedNames(zone.metadata[metadataDNSUpdate]); !slices.Equal(got, []string{"k", "other"}) {
		t.Fatalf("dnsupdate = %v, want [k other]", got)
	}
}

func TestZoneTSIGKeyApplyGivesUp(t *testing.T) {
	t.Parallel()
	fake := newFakePDNS(t)
	fake.addZone("z.test.")
	fake.addKey("k", secretA)

	// Every write is immediately undone.
	fake.afterWrite = func(f *fakePDNS, _ *http.Request) {
		delete(f.zones["z.test"].metadata, metadataDNSUpdate)
	}

	err := newTestAttachment(fake).apply(context.Background(), "z.test.", "k.", "k", tsigPurposes{DNSUpdate: true})
	if err == nil || !strings.Contains(err.Error(), "modified concurrently") {
		t.Fatalf("expected give-up error, got %v", err)
	}
}

func TestZoneTSIGKeyApplyCancelled(t *testing.T) {
	t.Parallel()
	fake := newFakePDNS(t)
	fake.addZone("z.test.")
	fake.addKey("k", secretA)

	ctx, cancel := context.WithCancel(context.Background())
	// Cancel during the settle delay after the first successful write.
	fake.afterWrite = func(*fakePDNS, *http.Request) { cancel() }

	if err := newTestAttachment(fake).apply(ctx, "z.test.", "k.", "k", tsigPurposes{AXFR: true}); err == nil {
		t.Fatal("expected context error")
	}
}
