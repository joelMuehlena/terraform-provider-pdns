package provider

import (
	"context"
	"fmt"
	"slices"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/plancheck"
	"github.com/hashicorp/terraform-plugin-testing/terraform"
	"github.com/samber/lo"
	"gitlab.com/joelMuehlena/homelab/code/terraform/provider/terraform-provider-pdns/internal/pdns_client"
)

func accRecordConfig(env *accEnv, zone, body string) string {
	return env.providerConfig() + fmt.Sprintf(`
resource "pdns_record" "r" {
  zone = %q
  name = "www"
%s
}
`, zone, body)
}

// checkRRset asserts one rrset of a zone on the server. A nil records list
// asserts that the rrset does not exist.
func (e *accEnv) checkRRset(zoneName, name, typ string, ttl int64, records, comments []string) func(*terraform.State) error {
	return func(*terraform.State) error {
		zone, err := e.zone(zoneName)
		if err != nil {
			return err
		}
		rr, ok := lo.Find(zone.Rrsets, func(rr pdns_client.Rrset) bool { return rr.Name == name && rr.Type == typ })
		if records == nil {
			if ok {
				return fmt.Errorf("rrset %s %s still exists: %+v", typ, name, rr)
			}
			return nil
		}
		if !ok {
			return fmt.Errorf("rrset %s %s does not exist", typ, name)
		}
		gotRecords := lo.Map(rr.Records, func(r pdns_client.Record, _ int) string { return r.Content })
		gotComments := lo.Map(rr.Comments, func(c pdns_client.Comment, _ int) string { return c.Content })
		slices.Sort(gotRecords)
		if rr.TTL != ttl || !slices.Equal(gotRecords, records) || !slices.Equal(gotComments, comments) {
			return fmt.Errorf("rrset = ttl %d records %v comments %v, want ttl %d records %v comments %v",
				rr.TTL, gotRecords, gotComments, ttl, records, comments)
		}
		return nil
	}
}

func TestAccRecordResource(t *testing.T) {
	env := testAccPreCheck(t)
	zone := env.createZone(t, "record")
	www := "www." + zone

	updated := accRecordConfig(env, zone, `
  type     = "A"
  ttl      = 60
  records  = ["10.0.0.3"]
  comments = ["managed by terraform"]`)

	resource.ParallelTest(t, resource.TestCase{
		ProtoV6ProviderFactories: testProviderFactories,
		CheckDestroy:             env.checkRRset(zone, www, "A", 0, nil, nil),
		Steps: []resource.TestStep{
			{
				Config: accRecordConfig(env, zone, `
  type    = "A"
  records = ["10.0.0.1", "10.0.0.2"]`),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("pdns_record.r", "ttl", "1800"),
					resource.TestCheckNoResourceAttr("pdns_record.r", "comments"),
					env.checkRRset(zone, www, "A", 1800, []string{"10.0.0.1", "10.0.0.2"}, []string{}),
				),
			},
			{
				Config: updated,
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{plancheck.ExpectResourceAction("pdns_record.r", plancheck.ResourceActionUpdate)},
				},
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("pdns_record.r", "comments.0", "managed by terraform"),
					env.checkRRset(zone, www, "A", 60, []string{"10.0.0.3"}, []string{"managed by terraform"}),
				),
			},
			{
				// Records changed out of band are restored.
				PreConfig: func() {
					err := env.client.UpdateZoneRecords(context.Background(), zone, []pdns_client.Rrset{{
						Name: www, Type: "A", Changetype: "REPLACE", TTL: 60,
						Records: []pdns_client.Record{{Content: "10.9.9.9"}},
					}})
					if err != nil {
						t.Fatal(err)
					}
				},
				Config: updated,
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{plancheck.ExpectResourceAction("pdns_record.r", plancheck.ResourceActionUpdate)},
				},
				Check: env.checkRRset(zone, www, "A", 60, []string{"10.0.0.3"}, []string{"managed by terraform"}),
			},
			{
				// Deleted out of band: removed from state and re-created.
				PreConfig: func() {
					err := env.client.UpdateZoneRecords(context.Background(), zone, []pdns_client.Rrset{{
						Name: www, Type: "A", Changetype: "DELETE",
					}})
					if err != nil {
						t.Fatal(err)
					}
				},
				Config: updated,
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{plancheck.ExpectResourceAction("pdns_record.r", plancheck.ResourceActionCreate)},
				},
				Check: env.checkRRset(zone, www, "A", 60, []string{"10.0.0.3"}, []string{"managed by terraform"}),
			},
			{
				// Removed from the configuration: deleted on the server.
				Config: env.providerConfig(),
				Check:  env.checkRRset(zone, www, "A", 0, nil, nil),
			},
		},
	})
}

// TestAccRecordResourceRemoveComments checks that removing the comments of a
// record removes them on the server: PowerDNS keeps the comments of an rrset
// when a REPLACE carries no comments key.
func TestAccRecordResourceRemoveComments(t *testing.T) {
	env := testAccPreCheck(t)
	zone := env.createZone(t, "comments")
	www := "www." + zone

	resource.ParallelTest(t, resource.TestCase{
		ProtoV6ProviderFactories: testProviderFactories,
		CheckDestroy:             env.checkRRset(zone, www, "A", 0, nil, nil),
		Steps: []resource.TestStep{
			{
				Config: accRecordConfig(env, zone, `
  type     = "A"
  records  = ["10.0.0.1"]
  comments = ["one", "two"]`),
				Check: env.checkRRset(zone, www, "A", 1800, []string{"10.0.0.1"}, []string{"one", "two"}),
			},
			{
				PreConfig: func() {
					// The premise: a REPLACE without comments keeps them.
					err := env.client.UpdateZoneRecords(context.Background(), zone, []pdns_client.Rrset{{
						Name: www, Type: "A", Changetype: "REPLACE", TTL: 1800,
						Records: []pdns_client.Record{{Content: "10.0.0.1"}},
					}})
					if err != nil {
						t.Fatal(err)
					}
					if err := env.checkRRset(zone, www, "A", 1800, []string{"10.0.0.1"}, []string{"one", "two"})(nil); err != nil {
						t.Fatal(err)
					}
				},
				Config: accRecordConfig(env, zone, `
  type    = "A"
  records = ["10.0.0.1"]`),
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{plancheck.ExpectResourceAction("pdns_record.r", plancheck.ResourceActionUpdate)},
				},
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckNoResourceAttr("pdns_record.r", "comments"),
					env.checkRRset(zone, www, "A", 1800, []string{"10.0.0.1"}, []string{}),
				),
			},
			{
				Config: accRecordConfig(env, zone, `
  type     = "A"
  records  = ["10.0.0.1"]
  comments = ["one"]`),
				Check: env.checkRRset(zone, www, "A", 1800, []string{"10.0.0.1"}, []string{"one"}),
			},
			{
				// An explicitly empty list removes them too, without a diff.
				Config: accRecordConfig(env, zone, `
  type     = "A"
  records  = ["10.0.0.1"]
  comments = []`),
				Check: env.checkRRset(zone, www, "A", 1800, []string{"10.0.0.1"}, []string{}),
			},
		},
	})
}

func TestAccRecordResourceFullyQualifiedName(t *testing.T) {
	env := testAccPreCheck(t)
	zone := env.createZone(t, "fqdn")

	resource.ParallelTest(t, resource.TestCase{
		ProtoV6ProviderFactories: testProviderFactories,
		CheckDestroy:             env.checkRRset(zone, "api."+zone, "CNAME", 0, nil, nil),
		Steps: []resource.TestStep{{
			Config: env.providerConfig() + fmt.Sprintf(`
resource "pdns_record" "r" {
  zone    = %[1]q
  name    = "api.%[1]s"
  type    = "CNAME"
  records = ["www.%[1]s"]
}
`, zone),
			Check: env.checkRRset(zone, "api."+zone, "CNAME", 1800, []string{"www." + zone}, []string{}),
		}},
	})
}

func TestAccRecordResourceImport(t *testing.T) {
	env := testAccPreCheck(t)
	zone := env.createZone(t, "import")

	config := env.providerConfig() + fmt.Sprintf(`
resource "pdns_record" "relative" {
  zone     = %[1]q
  name     = "www"
  type     = "AAAA"
  ttl      = 60
  records  = ["2345:425:2ca1::567:5673:23b5", "::1"]
  comments = ["managed by terraform"]
}

resource "pdns_record" "fqdn" {
  zone    = %[1]q
  name    = "api.%[1]s"
  type    = "CNAME"
  records = ["www.%[1]s"]
}
`, zone)

	resource.ParallelTest(t, resource.TestCase{
		ProtoV6ProviderFactories: testProviderFactories,
		Steps: []resource.TestStep{
			{Config: config},
			{
				ResourceName:                         "pdns_record.relative",
				ImportState:                          true,
				ImportStateId:                        zone + ":www:AAAA",
				ImportStateVerify:                    true,
				ImportStateVerifyIdentifierAttribute: "name",
			},
			{
				ResourceName:                         "pdns_record.fqdn",
				ImportState:                          true,
				ImportStateId:                        zone + ":api." + zone + ":CNAME",
				ImportStateVerify:                    true,
				ImportStateVerifyIdentifierAttribute: "name",
			},
		},
	})
}

// Changing the type replaces the record and removes the rrset of the old type.
func TestAccRecordResourceTypeChange(t *testing.T) {
	env := testAccPreCheck(t)
	zone := env.createZone(t, "typechange")
	www := "www." + zone

	resource.ParallelTest(t, resource.TestCase{
		ProtoV6ProviderFactories: testProviderFactories,
		CheckDestroy:             env.checkRRset(zone, www, "AAAA", 0, nil, nil),
		Steps: []resource.TestStep{
			{
				Config: accRecordConfig(env, zone, `
  type    = "A"
  records = ["10.0.0.1"]`),
				Check: env.checkRRset(zone, www, "A", 1800, []string{"10.0.0.1"}, []string{}),
			},
			{
				Config: accRecordConfig(env, zone, `
  type    = "AAAA"
  records = ["::1"]`),
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{plancheck.ExpectResourceAction("pdns_record.r", plancheck.ResourceActionDestroyBeforeCreate)},
				},
				Check: resource.ComposeAggregateTestCheckFunc(
					env.checkRRset(zone, www, "AAAA", 1800, []string{"::1"}, []string{}),
					env.checkRRset(zone, www, "A", 0, nil, nil),
				),
			},
		},
	})
}

// PowerDNS returns records sorted and in canonical form. Records configured in
// another order or spelling must converge, while a change on the server must
// still be detected.
func TestAccRecordResourceServerOrderAndCanonicalForm(t *testing.T) {
	env := testAccPreCheck(t)
	zone := env.createZone(t, "canonical")
	www := "www." + zone

	config := env.providerConfig() + fmt.Sprintf(`
resource "pdns_record" "a" {
  zone    = %[1]q
  name    = "www"
  type    = "A"
  records = ["10.0.0.9", "10.0.0.1", "10.0.0.5"]
}

resource "pdns_record" "aaaa" {
  zone    = %[1]q
  name    = "www"
  type    = "AAAA"
  records = ["2345:0425:2CA1:0000:0000:0567:5673:23b5", "::1"]
}

resource "pdns_record" "mx" {
  zone    = %[1]q
  name    = "mail"
  type    = "MX"
  # Out of order. Extra whitespace is not used here: 5.1 normalizes it, while
  # 4.9 and 5.0 reject it with 422 "Not in expected format".
  records = ["20 mx2.%[1]s", "10 mx1.%[1]s"]
}
`, zone)

	resource.ParallelTest(t, resource.TestCase{
		ProtoV6ProviderFactories: testProviderFactories,
		Steps: []resource.TestStep{
			{
				// The step fails if the refresh plan after apply is not empty.
				Config: config,
				Check: resource.ComposeAggregateTestCheckFunc(
					env.checkRRset(zone, www, "A", 1800, []string{"10.0.0.1", "10.0.0.5", "10.0.0.9"}, []string{}),
					env.checkRRset(zone, www, "AAAA", 1800, []string{"2345:425:2ca1::567:5673:23b5", "::1"}, []string{}),
					env.checkRRset(zone, "mail."+zone, "MX", 1800, []string{"10 mx1." + zone, "20 mx2." + zone}, []string{}),
				),
			},
			{
				// A record changed on the server is detected and restored.
				PreConfig: func() {
					err := env.client.UpdateZoneRecords(context.Background(), zone, []pdns_client.Rrset{{
						Name: www, Type: "AAAA", Changetype: "REPLACE", TTL: 1800,
						Records: []pdns_client.Record{{Content: "::1"}, {Content: "::2"}},
					}})
					if err != nil {
						t.Fatal(err)
					}
				},
				Config: config,
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{
						plancheck.ExpectResourceAction("pdns_record.aaaa", plancheck.ResourceActionUpdate),
						plancheck.ExpectResourceAction("pdns_record.a", plancheck.ResourceActionNoop),
						plancheck.ExpectResourceAction("pdns_record.mx", plancheck.ResourceActionNoop),
					},
				},
				Check: env.checkRRset(zone, www, "AAAA", 1800, []string{"2345:425:2ca1::567:5673:23b5", "::1"}, []string{}),
			},
		},
	})
}
