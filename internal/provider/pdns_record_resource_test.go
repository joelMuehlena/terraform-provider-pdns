package provider

import (
	"fmt"
	"os"
	"regexp"
	"slices"
	"testing"

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
				ImportStateId:                        "example.test./www/A",
				ImportStateVerify:                    true,
				ImportStateVerifyIdentifierAttribute: "name",
			},
			{
				// The zone may be given without the trailing dot.
				ResourceName:                         "pdns_record.fqdn",
				ImportState:                          true,
				ImportStateId:                        "example.test/api.example.test./CNAME",
				ImportStateVerify:                    true,
				ImportStateVerifyIdentifierAttribute: "name",
			},
			{
				ResourceName:  "pdns_record.relative",
				ImportState:   true,
				ImportStateId: "example.test./missing/A",
				ExpectError:   regexp.MustCompile(`Cannot import non-existent remote object`),
			},
			{
				ResourceName:  "pdns_record.relative",
				ImportState:   true,
				ImportStateId: "www",
				ExpectError:   regexp.MustCompile(`Expected an import ID of the form <zone>/<name>/<type>`),
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
