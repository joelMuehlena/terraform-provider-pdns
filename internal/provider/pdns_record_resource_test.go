package provider

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"regexp"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/hashicorp/terraform-plugin-framework/attr"
	fwresource "github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-go/tftypes"
	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/plancheck"
	"github.com/hashicorp/terraform-plugin-testing/terraform"
	"github.com/samber/lo"
	"gitlab.com/joelMuehlena/homelab/code/terraform/provider/terraform-provider-pdns/internal/pdns_client"
)

func recordConfig(fake *fakePDNS, body string) string {
	return fake.providerConfig() + `
resource "pdns_record" "r" {
  zone = "example.test."
  name = "www"
` + body + `
}
`
}

// checkFakeRRset asserts one rrset on the fake server.
func checkFakeRRset(fake *fakePDNS, name, typ string, ttl int64, records, comments []string) resource.TestCheckFunc {
	return func(*terraform.State) error {
		zone, ok := fake.zone("example.test.")
		if !ok {
			return fmt.Errorf("zone does not exist")
		}
		rr, ok := zone.rrset(name, typ)
		if !ok {
			return fmt.Errorf("rrset %s %s does not exist", typ, name)
		}
		gotRecords := lo.Map(rr.Records, func(r pdns_client.Record, _ int) string { return r.Content })
		gotComments := lo.Map(rr.Comments, func(c pdns_client.Comment, _ int) string { return c.Content })
		if rr.TTL != ttl || !slices.Equal(gotRecords, records) || !slices.Equal(gotComments, comments) {
			return fmt.Errorf("rrset = ttl %d records %v comments %v, want ttl %d records %v comments %v",
				rr.TTL, gotRecords, gotComments, ttl, records, comments)
		}
		return nil
	}
}

func TestRecordResource(t *testing.T) {
	t.Parallel()
	fake := newFakePDNS(t)
	fake.addZone("example.test.")

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: testProviderFactories,
		CheckDestroy: func(*terraform.State) error {
			zone, _ := fake.zone("example.test.")
			if _, ok := zone.rrset("www.example.test.", "A"); ok {
				return fmt.Errorf("rrset still exists")
			}
			return nil
		},
		Steps: []resource.TestStep{
			{
				Config: recordConfig(fake, `
  type    = "A"
  records = ["10.0.0.1", "10.0.0.2"]`),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("pdns_record.r", "ttl", "1800"),
					resource.TestCheckNoResourceAttr("pdns_record.r", "comments"),
					checkFakeRRset(fake, "www.example.test.", "A", 1800, []string{"10.0.0.1", "10.0.0.2"}, nil),
				),
			},
			{
				Config: recordConfig(fake, `
  type     = "A"
  ttl      = 60
  records  = ["10.0.0.3"]
  comments = ["managed by terraform"]`),
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{plancheck.ExpectResourceAction("pdns_record.r", plancheck.ResourceActionUpdate)},
				},
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("pdns_record.r", "comments.0", "managed by terraform"),
					checkFakeRRset(fake, "www.example.test.", "A", 60, []string{"10.0.0.3"}, []string{"managed by terraform"}),
				),
			},
			{
				// Records changed out of band are restored.
				PreConfig: func() {
					fake.mutate(func(f *fakePDNS) {
						z := f.zones["example.test"]
						for i := range z.rrsets {
							if z.rrsets[i].Type == "A" {
								z.rrsets[i].Records = []pdns_client.Record{{Content: "10.9.9.9"}}
							}
						}
					})
				},
				Config: recordConfig(fake, `
  type     = "A"
  ttl      = 60
  records  = ["10.0.0.3"]
  comments = ["managed by terraform"]`),
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{plancheck.ExpectResourceAction("pdns_record.r", plancheck.ResourceActionUpdate)},
				},
				Check: checkFakeRRset(fake, "www.example.test.", "A", 60, []string{"10.0.0.3"}, []string{"managed by terraform"}),
			},
			{
				// Deleted out of band: removed from state and re-created.
				PreConfig: func() {
					fake.mutate(func(f *fakePDNS) {
						z := f.zones["example.test"]
						z.rrsets = slices.DeleteFunc(z.rrsets, func(rr pdns_client.Rrset) bool { return rr.Type == "A" })
					})
				},
				Config: recordConfig(fake, `
  type     = "A"
  ttl      = 60
  records  = ["10.0.0.3"]
  comments = ["managed by terraform"]`),
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{plancheck.ExpectResourceAction("pdns_record.r", plancheck.ResourceActionCreate)},
				},
				Check: checkFakeRRset(fake, "www.example.test.", "A", 60, []string{"10.0.0.3"}, []string{"managed by terraform"}),
			},
		},
	})
}

// TestRecordResourceRemoveComments checks that removing the comments of a
// record removes them on the server. PowerDNS keeps the comments of an rrset
// when a REPLACE carries none, so the provider must send an empty list.
func TestRecordResourceRemoveComments(t *testing.T) {
	t.Parallel()
	fake := newFakePDNS(t)
	fake.addZone("example.test.")
	www := "www.example.test."

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: testProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: recordConfig(fake, `
  type     = "A"
  records  = ["10.0.0.1"]
  comments = ["one", "two"]`),
				Check: checkFakeRRset(fake, www, "A", 1800, []string{"10.0.0.1"}, []string{"one", "two"}),
			},
			{
				Config: recordConfig(fake, `
  type    = "A"
  records = ["10.0.0.1"]`),
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{plancheck.ExpectResourceAction("pdns_record.r", plancheck.ResourceActionUpdate)},
				},
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckNoResourceAttr("pdns_record.r", "comments"),
					checkFakeRRset(fake, www, "A", 1800, []string{"10.0.0.1"}, nil),
				),
			},
			{
				// Comments added out of band are removed again.
				PreConfig: func() {
					fake.mutate(func(f *fakePDNS) {
						z := f.zones["example.test"]
						for i := range z.rrsets {
							if z.rrsets[i].Type == "A" {
								z.rrsets[i].Comments = []pdns_client.Comment{{Content: "out of band"}}
							}
						}
					})
				},
				Config: recordConfig(fake, `
  type    = "A"
  records = ["10.0.0.1"]`),
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{plancheck.ExpectResourceAction("pdns_record.r", plancheck.ResourceActionUpdate)},
				},
				Check: checkFakeRRset(fake, www, "A", 1800, []string{"10.0.0.1"}, nil),
			},
			{
				// An explicitly empty list is the same and does not diff.
				Config: recordConfig(fake, `
  type     = "A"
  records  = ["10.0.0.1"]
  comments = ["one"]`),
				Check: checkFakeRRset(fake, www, "A", 1800, []string{"10.0.0.1"}, []string{"one"}),
			},
			{
				Config: recordConfig(fake, `
  type     = "A"
  records  = ["10.0.0.1"]
  comments = []`),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("pdns_record.r", "comments.#", "0"),
					checkFakeRRset(fake, www, "A", 1800, []string{"10.0.0.1"}, nil),
				),
			},
		},
	})
}

func TestRecordResourceFullyQualifiedName(t *testing.T) {
	t.Parallel()
	fake := newFakePDNS(t)
	fake.addZone("example.test.")

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: testProviderFactories,
		Steps: []resource.TestStep{{
			Config: fake.providerConfig() + `
resource "pdns_record" "r" {
  zone    = "example.test."
  name    = "api.example.test."
  type    = "CNAME"
  records = ["www.example.test."]
}
`,
			Check: checkFakeRRset(fake, "api.example.test.", "CNAME", 1800, []string{"www.example.test."}, nil),
		}},
	})
}

func TestRecordResourceErrors(t *testing.T) {
	t.Parallel()

	tests := map[string]struct {
		body    string
		wantErr string
		plan    bool
	}{
		"no records":        {`type = "A"` + "\n" + `records = []`, `at least 1`, true},
		"duplicate records": {`type = "A"` + "\n" + `records = ["10.0.0.1", "10.0.0.1"]`, `Duplicate List Value`, true},
		"empty record":      {`type = "A"` + "\n" + `records = [""]`, `at least 1`, true},
		"lowercase type":    {`type = "a"` + "\n" + `records = ["10.0.0.1"]`, `must be an uppercase record type`, true},
		"zone missing":      {`type = "A"` + "\n" + `records = ["10.0.0.1"]`, `Zone not found`, false},
	}

	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			fake := newFakePDNS(t)
			resource.UnitTest(t, resource.TestCase{
				ProtoV6ProviderFactories: testProviderFactories,
				Steps: []resource.TestStep{{
					Config:      recordConfig(fake, tt.body),
					PlanOnly:    tt.plan,
					ExpectError: regexp.MustCompile(tt.wantErr),
				}},
			})
		})
	}
}

// PowerDNS returns the records of an rrset sorted and in canonical form; the
// configured order and spelling must still converge to an empty plan, while a
// real change on the server must still show up.
func TestRecordResourceServerOrderAndCanonicalForm(t *testing.T) {
	t.Parallel()
	fake := newFakePDNS(t)
	fake.addZone("example.test.")

	config := fake.providerConfig() + `
resource "pdns_record" "a" {
  zone    = "example.test."
  name    = "www"
  type    = "A"
  records = ["10.0.0.9", "10.0.0.1", "10.0.0.5"]
}

resource "pdns_record" "aaaa" {
  zone    = "example.test."
  name    = "www"
  type    = "AAAA"
  records = ["2345:0425:2CA1:0000:0000:0567:5673:23b5", "::1"]
}

resource "pdns_record" "mx" {
  zone    = "example.test."
  name    = "mail"
  type    = "MX"
  records = ["20   mx2.example.test.", "10 mx1.example.test."]
}
`

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: testProviderFactories,
		Steps: []resource.TestStep{
			{
				// The test framework fails the step if the plan after apply
				// (which refreshes) is not empty.
				Config: config,
				Check: resource.ComposeAggregateTestCheckFunc(
					checkFakeRRset(fake, "www.example.test.", "A", 1800, []string{"10.0.0.1", "10.0.0.5", "10.0.0.9"}, nil),
					checkFakeRRset(fake, "www.example.test.", "AAAA", 1800, []string{"2345:425:2ca1::567:5673:23b5", "::1"}, nil),
					checkFakeRRset(fake, "mail.example.test.", "MX", 1800, []string{"10 mx1.example.test.", "20 mx2.example.test."}, nil),
					resource.TestCheckResourceAttr("pdns_record.a", "records.0", "10.0.0.9"),
					resource.TestCheckResourceAttr("pdns_record.aaaa", "records.0", "2345:0425:2CA1:0000:0000:0567:5673:23b5"),
				),
			},
			{
				// A record changed on the server is detected and restored.
				PreConfig: func() {
					fake.mutate(func(f *fakePDNS) {
						z := f.zones["example.test"]
						for i := range z.rrsets {
							if z.rrsets[i].Type == "AAAA" {
								z.rrsets[i].Records = []pdns_client.Record{{Content: "::1"}, {Content: "::2"}}
							}
						}
					})
				},
				Config: config,
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{
						plancheck.ExpectResourceAction("pdns_record.aaaa", plancheck.ResourceActionUpdate),
						plancheck.ExpectResourceAction("pdns_record.a", plancheck.ResourceActionNoop),
						plancheck.ExpectResourceAction("pdns_record.mx", plancheck.ResourceActionNoop),
					},
				},
				Check: checkFakeRRset(fake, "www.example.test.", "AAAA", 1800, []string{"2345:425:2ca1::567:5673:23b5", "::1"}, nil),
			},
		},
	})
}

// Changing the type replaces the rrset: the rrset of the old type must not be
// left behind on the server.
func TestRecordResourceTypeChange(t *testing.T) {
	t.Parallel()
	fake := newFakePDNS(t)
	fake.addZone("example.test.")

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: testProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: recordConfig(fake, `
  type    = "A"
  records = ["10.0.0.1"]`),
				Check: checkFakeRRset(fake, "www.example.test.", "A", 1800, []string{"10.0.0.1"}, nil),
			},
			{
				Config: recordConfig(fake, `
  type    = "AAAA"
  records = ["::1"]`),
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{plancheck.ExpectResourceAction("pdns_record.r", plancheck.ResourceActionDestroyBeforeCreate)},
				},
				Check: resource.ComposeAggregateTestCheckFunc(
					checkFakeRRset(fake, "www.example.test.", "AAAA", 1800, []string{"::1"}, nil),
					func(*terraform.State) error {
						zone, _ := fake.zone("example.test.")
						if rr, ok := zone.rrset("www.example.test.", "A"); ok {
							return fmt.Errorf("rrset of the old type still exists: %+v", rr)
						}
						return nil
					},
				),
			},
		},
	})
}

func TestRecordResourceImport(t *testing.T) {
	t.Parallel()
	fake := newFakePDNS(t)
	fake.addZone("example.test.")

	config := fake.providerConfig() + `
resource "pdns_record" "relative" {
  zone     = "example.test."
  name     = "www"
  type     = "A"
  ttl      = 60
  records  = ["10.0.0.1", "10.0.0.2"]
  comments = ["managed by terraform"]
}

resource "pdns_record" "fqdn" {
  zone    = "example.test."
  name    = "api.example.test."
  type    = "CNAME"
  records = ["www.example.test."]
}
`

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: testProviderFactories,
		Steps: []resource.TestStep{
			{Config: config},
			{
				ResourceName:                         "pdns_record.relative",
				ImportState:                          true,
				ImportStateId:                        "example.test.:www:A",
				ImportStateVerify:                    true,
				ImportStateVerifyIdentifierAttribute: "name",
			},
			{
				// The zone may be given without the trailing dot.
				ResourceName:                         "pdns_record.fqdn",
				ImportState:                          true,
				ImportStateId:                        "example.test:api.example.test.:CNAME",
				ImportStateVerify:                    true,
				ImportStateVerifyIdentifierAttribute: "name",
			},
			{
				ResourceName:  "pdns_record.relative",
				ImportState:   true,
				ImportStateId: "example.test.:missing:A",
				ExpectError:   regexp.MustCompile(`Cannot import non-existent remote object`),
			},
			{
				ResourceName:  "pdns_record.relative",
				ImportState:   true,
				ImportStateId: "www",
				ExpectError:   regexp.MustCompile(`Expected an import ID of the form <zone>:<name>:<type>`),
			},
		},
	})
}

func TestRecordResourceImportClasslessReverseZone(t *testing.T) {
	t.Parallel()
	fake := newFakePDNS(t)
	// RFC 2317: "/" is part of the zone and of fully qualified record names.
	const zone = "0/26.2.0.192.in-addr.arpa."
	fake.addZone(zone)

	config := fake.providerConfig() + `
resource "pdns_record" "relative" {
  zone    = "` + zone + `"
  name    = "1"
  type    = "PTR"
  records = ["host1.example.test."]
}

resource "pdns_record" "fqdn" {
  zone    = "` + zone + `"
  name    = "2.` + zone + `"
  type    = "PTR"
  records = ["host2.example.test."]
}
`

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: testProviderFactories,
		Steps: []resource.TestStep{
			{Config: config},
			{
				Config:                               config,
				ResourceName:                         "pdns_record.relative",
				ImportState:                          true,
				ImportStateId:                        zone + ":1:PTR",
				ImportStateVerify:                    true,
				ImportStateVerifyIdentifierAttribute: "name",
			},
			{
				Config:                               config,
				ResourceName:                         "pdns_record.fqdn",
				ImportState:                          true,
				ImportStateId:                        zone + ":2." + zone + ":PTR",
				ImportStateVerify:                    true,
				ImportStateVerifyIdentifierAttribute: "name",
			},
		},
	})
}

// The documented example applies, converges, and creates the records only
// after their zone.
func TestRecordResourceExample(t *testing.T) {
	t.Parallel()
	example, err := os.ReadFile("../../examples/resources/pdns_record/resource.tf")
	if err != nil {
		t.Fatal(err)
	}
	fake := newFakePDNS(t)

	dependsOnZone := func(name string) resource.TestCheckFunc {
		return func(s *terraform.State) error {
			rs, ok := s.RootModule().Resources[name]
			if !ok {
				return fmt.Errorf("%s not in state", name)
			}
			if !slices.Contains(rs.Dependencies, "pdns_zone.example_com") {
				return fmt.Errorf("%s does not depend on pdns_zone.example_com, dependencies: %v", name, rs.Dependencies)
			}
			return nil
		}
	}

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: testProviderFactories,
		Steps: []resource.TestStep{{
			Config: fake.providerConfig() + string(example),
			Check: resource.ComposeAggregateTestCheckFunc(
				dependsOnZone("pdns_record.test_aaaa"),
				dependsOnZone("pdns_record.test2_a"),
			),
		}},
	})
}

func TestReconcileRecords(t *testing.T) {
	t.Parallel()

	tests := map[string]struct {
		typ           string
		prior, server []string
		want          []string
	}{
		"same":            {"A", []string{"10.0.0.1"}, []string{"10.0.0.1"}, []string{"10.0.0.1"}},
		"server order":    {"A", []string{"10.0.0.9", "10.0.0.1"}, []string{"10.0.0.1", "10.0.0.9"}, []string{"10.0.0.9", "10.0.0.1"}},
		"canonical aaaa":  {"AAAA", []string{"2345:0425:2CA1:0000:0000:0567:5673:23b5"}, []string{"2345:425:2ca1::567:5673:23b5"}, []string{"2345:0425:2CA1:0000:0000:0567:5673:23b5"}},
		"mx whitespace":   {"MX", []string{"10   mx."}, []string{"10 mx."}, []string{"10   mx."}},
		"txt whitespace":  {"TXT", []string{`"a  b"`}, []string{`"a b"`}, []string{`"a b"`}},
		"txt strings":     {"TXT", []string{`"a  b"   "c"`}, []string{`"a  b" "c"`}, []string{`"a  b"   "c"`}},
		"import":          {"A", nil, []string{"10.0.0.1"}, []string{"10.0.0.1"}},
		"changed":         {"A", []string{"10.0.0.9", "10.0.0.1"}, []string{"10.0.0.1", "10.0.0.2"}, []string{"10.0.0.1", "10.0.0.2"}},
		"changed aaaa":    {"AAAA", []string{"::0001", "::3"}, []string{"::1", "::2"}, []string{"::0001", "::2"}},
		"record added":    {"A", []string{"10.0.0.1"}, []string{"10.0.0.1", "10.0.0.2"}, []string{"10.0.0.1", "10.0.0.2"}},
		"record removed":  {"A", []string{"10.0.0.1", "10.0.0.2"}, []string{"10.0.0.2"}, []string{"10.0.0.2"}},
		"duplicate prior": {"AAAA", []string{"::1", "::0001"}, []string{"::1"}, []string{"::1"}},
	}

	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			if got := reconcileRecords(tt.typ, tt.prior, tt.server); !slices.Equal(got, tt.want) {
				t.Errorf("reconcileRecords(%q, %q, %q) = %q, want %q", tt.typ, tt.prior, tt.server, got, tt.want)
			}
		})
	}
}

func TestCanonicalCharacterStrings(t *testing.T) {
	t.Parallel()

	for in, want := range map[string]string{
		`"a"`:                 `"a"`,
		`"a"   "b"`:           `"a" "b"`,
		"\t \"a\"\t\n\"b\"  ": `"a" "b"`,
		`"a  b"   "c"`:        `"a  b" "c"`,
		`"a\"  b"   "c"`:      `"a\"  b" "c"`,
		`"a\\"   "b"`:         `"a\\" "b"`,
		`"a\\\"  "   "b"`:     `"a\\\"  " "b"`,
		`"v=spf1  -all"`:      `"v=spf1  -all"`,
		`"unterminated  x`:    `"unterminated  x`,
		``:                    ``,
	} {
		if got := canonicalCharacterStrings(in); got != want {
			t.Errorf("canonicalCharacterStrings(%q) = %q, want %q", in, got, want)
		}
	}
	for _, typ := range []string{"TXT", "spf"} {
		if got := canonicalRecordContent(typ, `"a"  "b  c"`); got != `"a" "b  c"` {
			t.Errorf("canonicalRecordContent(%s) = %q", typ, got)
		}
	}
}

// TestRecordResourceTXTWhitespace checks that whitespace between the quoted
// strings of a TXT record, which PowerDNS collapses, does not diff forever.
func TestRecordResourceTXTWhitespace(t *testing.T) {
	t.Parallel()
	fake := newFakePDNS(t)
	fake.addZone("example.test.")

	config := recordConfig(fake, `
  type    = "TXT"
  records = ["\"a  b\"   \"c\"", "\"v=spf1\"\t\"-all\""]`)

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: testProviderFactories,
		Steps: []resource.TestStep{
			{
				// The step fails if the plan after apply is not empty.
				Config: config,
				Check: resource.ComposeAggregateTestCheckFunc(
					checkFakeRRset(fake, "www.example.test.", "TXT", 1800, []string{`"a  b" "c"`, `"v=spf1" "-all"`}, nil),
					resource.TestCheckResourceAttr("pdns_record.r", "records.0", `"a  b"   "c"`),
				),
			},
			{
				// Whitespace inside a string is significant.
				PreConfig: func() {
					fake.mutate(func(f *fakePDNS) {
						z := f.zones["example.test"]
						for i := range z.rrsets {
							if z.rrsets[i].Type == "TXT" {
								z.rrsets[i].Records = []pdns_client.Record{{Content: `"a b" "c"`}, {Content: `"v=spf1" "-all"`}}
							}
						}
					})
				},
				Config: config,
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{plancheck.ExpectResourceAction("pdns_record.r", plancheck.ResourceActionUpdate)},
				},
				Check: checkFakeRRset(fake, "www.example.test.", "TXT", 1800, []string{`"a  b" "c"`, `"v=spf1" "-all"`}, nil),
			},
		},
	})
}

func TestRecordResourceParallelChangesAreSerializedPerZone(t *testing.T) {
	t.Parallel()
	fake := newFakePDNS(t)
	fake.addZone("example.test.")

	// gpgsql deadlocks on concurrent PATCHes of one zone, so the provider must
	// never send two at once, even with Terraform's default parallelism.
	var inFlight, maxInFlight atomic.Int32
	fake.onPatch = func(*http.Request) {
		n := inFlight.Add(1)
		defer inFlight.Add(-1)
		for {
			m := maxInFlight.Load()
			if n <= m || maxInFlight.CompareAndSwap(m, n) {
				break
			}
		}
		time.Sleep(20 * time.Millisecond)
	}

	config := fake.providerConfig()
	for i := range 10 {
		config += fmt.Sprintf(`
resource "pdns_record" "r%[1]d" {
  zone    = "example.test."
  name    = "r%[1]d"
  type    = "A"
  records = ["10.0.0.%[2]d"]
}
`, i, i+1)
	}

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: testProviderFactories,
		CheckDestroy: func(*terraform.State) error {
			if got := maxInFlight.Load(); got != 1 {
				return fmt.Errorf("up to %d PATCH requests to one zone ran concurrently, want 1", got)
			}
			return nil
		},
		Steps: []resource.TestStep{{
			Config: config,
			Check: func(*terraform.State) error {
				if got := maxInFlight.Load(); got != 1 {
					return fmt.Errorf("up to %d PATCH requests to one zone ran concurrently, want 1", got)
				}
				return nil
			},
		}},
	})
}

// TestRecordResourceReadWarnsWhenDeleted checks that a record deleted out of
// band is removed from state with a warning, like the other resources.
func TestRecordResourceReadWarnsWhenDeleted(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	fake := newFakePDNS(t)
	fake.addZone("example.test.")

	r := &RecordResource{providerData: &PDNSProviderData{
		pdnsClient: pdns_client.NewPDNSClient(fake.client, fake.srv.URL, "localhost", fakeAPIKey, pdns_client.WithServerCheck()),
	}}
	schemaResp := &fwresource.SchemaResponse{}
	r.Schema(ctx, fwresource.SchemaRequest{}, schemaResp)

	state := tfsdk.State{Schema: schemaResp.Schema, Raw: tftypes.NewValue(schemaResp.Schema.Type().TerraformType(ctx), nil)}
	diags := state.Set(ctx, &RecordResourceModel{
		Zone:     types.StringValue("example.test."),
		Name:     types.StringValue("www"),
		Type:     types.StringValue("A"),
		TTL:      types.Int64Value(60),
		Records:  types.ListValueMust(types.StringType, []attr.Value{types.StringValue("10.0.0.1")}),
		Comments: types.ListNull(types.StringType),
	})
	if diags.HasError() {
		t.Fatal(diags)
	}

	resp := &fwresource.ReadResponse{State: state}
	r.Read(ctx, fwresource.ReadRequest{State: state}, resp)

	if resp.Diagnostics.HasError() {
		t.Fatalf("unexpected error: %v", resp.Diagnostics)
	}
	if !resp.State.Raw.IsNull() {
		t.Error("the record was not removed from state")
	}
	warnings := resp.Diagnostics.Warnings()
	if len(warnings) != 1 || warnings[0].Summary() != "Record not found" ||
		!strings.Contains(warnings[0].Detail(), `The A record "www.example.test." no longer exists`) {
		t.Errorf("warnings = %v", warnings)
	}
}
