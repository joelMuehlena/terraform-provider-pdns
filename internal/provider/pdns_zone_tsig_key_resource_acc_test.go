package provider

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/plancheck"
	"github.com/hashicorp/terraform-plugin-testing/terraform"
)

// accZoneTSIG is the setup of the zone TSIG acceptance tests: three zones,
// three keys attached to every zone (count index = zone*3 + key) and a
// fourth key attached to the first zone only.
type accZoneTSIG struct {
	env   *accEnv
	zones []string
	keys  []string
}

func newAccZoneTSIG(t *testing.T, env *accEnv) *accZoneTSIG {
	t.Helper()
	s := &accZoneTSIG{env: env}
	for _, label := range []string{"z1", "z2", "z3"} {
		s.zones = append(s.zones, env.createZone(t, label))
	}
	for _, label := range []string{"a", "b", "c", "d"} {
		s.keys = append(s.keys, env.keyName(t, label))
	}
	return s
}

// config renders the setup. The counted attachments have dnsupdate and
// axfr, except axfr for the count indexes in axfrOff. The fourth key is the
// secondary key of the first zone (PowerDNS allows only one per zone).
func (s *accZoneTSIG) config(axfrOff ...int) string {
	quoted := func(names []string) string {
		return `["` + strings.Join(names, `", "`) + `"]`
	}
	off := make([]string, 0, len(axfrOff))
	for _, i := range axfrOff {
		off = append(off, fmt.Sprint(i))
	}
	return s.env.providerConfig() + fmt.Sprintf(`
locals {
  zones    = %[1]s
  keys     = %[2]s
  pairs    = setproduct(local.zones, local.keys)
  axfr_off = [%[3]s]
}

resource "pdns_tsig_key" "k" {
  count = length(local.keys)
  name  = local.keys[count.index]
  key   = %[4]q
}

resource "pdns_tsig_key" "d" {
  name = %[5]q
  key  = %[6]q
}

resource "pdns_zone_tsig_key" "all" {
  count       = length(local.pairs)
  zone        = local.pairs[count.index][0]
  tsig_key_id = pdns_tsig_key.k[index(local.keys, local.pairs[count.index][1])].id
  dnsupdate   = true
  axfr        = !contains(local.axfr_off, count.index)
}

resource "pdns_zone_tsig_key" "d" {
  zone        = local.zones[0]
  tsig_key_id = pdns_tsig_key.d.id
  secondary   = true
}
`, quoted(s.zones), quoted(s.keys[:3]), strings.Join(off, ", "), secretA, s.keys[3], secretB)
}

// checkLists asserts the TSIG lists of a zone on the server, ignoring order
// and the spelling of the entries.
func (s *accZoneTSIG) checkLists(zoneName string, dnsupdate, master, slave []string) func(*terraform.State) error {
	return func(*terraform.State) error {
		ctx := context.Background()
		zone, err := s.env.client.GetZone(ctx, zoneName, false, "")
		if err != nil {
			return err
		}
		names, err := s.env.client.GetZoneMetadata(ctx, zoneName, metadataTSIGAllowDNSUpdate)
		if err != nil {
			return err
		}
		for name, pair := range map[string][2][]string{
			"dnsupdate": {names, dnsupdate},
			"master":    {zone.MasterTsigKeyIDS, master},
			"slave":     {zone.SlaveTsigKeyIDS, slave},
		} {
			if got, want := normalizedNames(pair[0]), normalizedNames(pair[1]); !slices.Equal(got, want) {
				return fmt.Errorf("zone %s %s list = %v, want %v", zoneName, name, got, want)
			}
		}
		return nil
	}
}

func (s *accZoneTSIG) checkAllEmpty() resource.TestCheckFunc {
	checks := make([]resource.TestCheckFunc, 0, len(s.zones))
	for _, z := range s.zones {
		checks = append(checks, s.checkLists(z, nil, nil, nil))
	}
	return resource.ComposeAggregateTestCheckFunc(checks...)
}

func TestAccZoneTSIGKeyResource(t *testing.T) {
	env := testAccPreCheck(t)
	s := newAccZoneTSIG(t, env)
	abc := s.keys[:3]
	z1, z2, z3 := s.zones[0], s.zones[1], s.zones[2]

	resource.ParallelTest(t, resource.TestCase{
		ProtoV6ProviderFactories: testProviderFactories,
		CheckDestroy: resource.ComposeAggregateTestCheckFunc(
			s.checkAllEmpty(),
			env.checkKeyGone(s.keys...),
		),
		Steps: []resource.TestStep{
			{
				// Many keys per zone and many zones per key in one apply.
				Config: s.config(),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("pdns_zone_tsig_key.all.0", "id", z1+"/"+abc[0]+"."),
					resource.TestCheckResourceAttr("pdns_zone_tsig_key.d", "dnsupdate", "false"),
					s.checkLists(z1, abc, abc, s.keys[3:]),
					s.checkLists(z2, abc, abc, nil),
					s.checkLists(z3, abc, abc, nil),
				),
			},
			{
				// Toggling a purpose updates in place and leaves the others alone.
				Config: s.config(0),
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{
						plancheck.ExpectResourceAction("pdns_zone_tsig_key.all[0]", plancheck.ResourceActionUpdate),
						plancheck.ExpectResourceAction("pdns_zone_tsig_key.all[1]", plancheck.ResourceActionNoop),
					},
				},
				Check: resource.ComposeAggregateTestCheckFunc(
					s.checkLists(z1, abc, abc[1:], s.keys[3:]),
					s.checkLists(z2, abc, abc, nil),
				),
			},
			{
				ResourceName:      "pdns_zone_tsig_key.d",
				ImportState:       true,
				ImportStateId:     z1 + "/" + s.keys[3] + ".",
				ImportStateVerify: true,
			},
		},
	})
}

func TestAccZoneTSIGKeyResourceDrift(t *testing.T) {
	env := testAccPreCheck(t)
	s := newAccZoneTSIG(t, env)
	abc := s.keys[:3]
	z1, z2, z3 := s.zones[0], s.zones[1], s.zones[2]
	config := s.config()
	ctx := context.Background()

	resource.ParallelTest(t, resource.TestCase{
		ProtoV6ProviderFactories: testProviderFactories,
		CheckDestroy:             s.checkAllEmpty(),
		Steps: []resource.TestStep{
			{Config: config},
			{
				// One purpose removed out of band per zone: drift on that
				// attachment and flag only.
				PreConfig: func() {
					// z1/b loses dnsupdate.
					if err := env.client.SetZoneMetadata(ctx, z1, metadataTSIGAllowDNSUpdate, []string{abc[0], abc[2]}); err != nil {
						t.Fatal(err)
					}
					// z2/c loses axfr.
					master := []string{abc[0] + ".", abc[1] + "."}
					if err := env.client.UpdateZoneTSIGKeyIDs(ctx, z2, &master, nil); err != nil {
						t.Fatal(err)
					}
					// z1/d loses secondary, its only purpose.
					if err := env.client.UpdateZoneTSIGKeyIDs(ctx, z1, nil, &[]string{}); err != nil {
						t.Fatal(err)
					}
				},
				Config: config,
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{
						plancheck.ExpectResourceAction("pdns_zone_tsig_key.all[1]", plancheck.ResourceActionUpdate),
						plancheck.ExpectResourceAction("pdns_zone_tsig_key.all[5]", plancheck.ResourceActionUpdate),
						plancheck.ExpectResourceAction("pdns_zone_tsig_key.d", plancheck.ResourceActionCreate),
						plancheck.ExpectResourceAction("pdns_zone_tsig_key.all[0]", plancheck.ResourceActionNoop),
						plancheck.ExpectResourceAction("pdns_zone_tsig_key.all[4]", plancheck.ResourceActionNoop),
					},
				},
				Check: resource.ComposeAggregateTestCheckFunc(
					s.checkLists(z1, abc, abc, s.keys[3:]),
					s.checkLists(z2, abc, abc, nil),
					s.checkLists(z3, abc, abc, nil),
				),
			},
		},
	})
}
