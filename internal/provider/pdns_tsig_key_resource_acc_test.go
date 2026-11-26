package provider

import (
	"context"
	"fmt"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/plancheck"
	"github.com/hashicorp/terraform-plugin-testing/tfversion"
	"gitlab.com/joelMuehlena/homelab/code/terraform/provider/terraform-provider-pdns/internal/pdns_client"
)

func accTSIGKeyConfig(env *accEnv, name, body string) string {
	return env.providerConfig() + fmt.Sprintf(`
resource "pdns_tsig_key" "k" {
  name = %q
%s
}
`, name, body)
}

func TestAccTSIGKeyResource(t *testing.T) {
	env := testAccPreCheck(t)
	name := env.keyName(t, "key")

	resource.ParallelTest(t, resource.TestCase{
		ProtoV6ProviderFactories: testProviderFactories,
		CheckDestroy:             env.checkKeyGone(name),
		Steps: []resource.TestStep{
			{
				Config: accTSIGKeyConfig(env, name, `key = "`+secretA+`"`),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("pdns_tsig_key.k", "id", name+"."),
					resource.TestCheckResourceAttr("pdns_tsig_key.k", "algorithm", "hmac-sha256"),
					resource.TestCheckResourceAttr("pdns_tsig_key.k", "key", secretA),
					env.checkKey(name, "hmac-sha256", secretA),
				),
			},
			env.tsigKeyUpdateStep(t, resource.TestStep{
				// A new secret updates the key in place.
				Config: accTSIGKeyConfig(env, name, `key = "`+secretB+`"`),
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{plancheck.ExpectResourceAction("pdns_tsig_key.k", plancheck.ResourceActionUpdate)},
				},
				Check: env.checkKey(name, "hmac-sha256", secretB),
			}),
			{
				// A new algorithm replaces the key. An in-place PUT would leave
				// the old (name, algorithm) row behind and GET could still
				// return the old algorithm.
				Config: accTSIGKeyConfig(env, name, `
  algorithm = "hmac-sha512"
  key       = "`+secretB+`"`),
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{plancheck.ExpectResourceAction("pdns_tsig_key.k", plancheck.ResourceActionDestroyBeforeCreate)},
				},
				Check: env.checkKey(name, "hmac-sha512", secretB),
			},
			{
				ResourceName:      "pdns_tsig_key.k",
				ImportState:       true,
				ImportStateId:     name + ".",
				ImportStateVerify: true,
			},
		},
	})
}

func TestAccTSIGKeyResourceWriteOnly(t *testing.T) {
	env := testAccPreCheck(t)
	name := env.keyName(t, "wo")

	config := func(secret string, version int) string {
		return accTSIGKeyConfig(env, name, fmt.Sprintf(`
  key_wo         = %q
  key_wo_version = %d`, secret, version))
	}

	resource.ParallelTest(t, resource.TestCase{
		TerraformVersionChecks:   []tfversion.TerraformVersionCheck{tfversion.SkipBelow(tfversion.Version1_11_0)},
		ProtoV6ProviderFactories: testProviderFactories,
		CheckDestroy:             env.checkKeyGone(name),
		Steps: []resource.TestStep{
			{
				Config: config(secretA, 1),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckNoResourceAttr("pdns_tsig_key.k", "key"),
					resource.TestCheckNoResourceAttr("pdns_tsig_key.k", "key_wo"),
					resource.TestCheckResourceAttr("pdns_tsig_key.k", "key_wo_version", "1"),
					env.checkKey(name, "hmac-sha256", secretA),
				),
			},
			{
				// A new secret without a version bump is not applied.
				Config: config(secretB, 1),
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{plancheck.ExpectEmptyPlan()},
				},
				Check: env.checkKey(name, "hmac-sha256", secretA),
			},
			env.tsigKeyUpdateStep(t, resource.TestStep{
				// Bumping the version rotates the secret in place.
				Config: config(secretB, 2),
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{plancheck.ExpectResourceAction("pdns_tsig_key.k", plancheck.ResourceActionUpdate)},
				},
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckNoResourceAttr("pdns_tsig_key.k", "key"),
					resource.TestCheckNoResourceAttr("pdns_tsig_key.k", "key_wo"),
					env.checkKey(name, "hmac-sha256", secretB),
				),
			}),
		},
	})
}

func TestAccTSIGKeyResourceDrift(t *testing.T) {
	env := testAccPreCheck(t)
	name := env.keyName(t, "drift")
	config := accTSIGKeyConfig(env, name, `key = "`+secretA+`"`)

	resource.ParallelTest(t, resource.TestCase{
		ProtoV6ProviderFactories: testProviderFactories,
		CheckDestroy:             env.checkKeyGone(name),
		Steps: []resource.TestStep{
			{Config: config},
			env.tsigKeyUpdateStep(t, resource.TestStep{
				// Changed out of band (re-created, as not every server can
				// update a key in place): restored in place.
				PreConfig: func() {
					ctx := context.Background()
					if err := env.client.DeleteTSIGKey(ctx, name+"."); err != nil {
						t.Fatal(err)
					}
					if _, err := env.client.CreateTSIGKey(ctx, pdns_client.TSIGKey{Name: name, Algorithm: "hmac-sha256", Key: secretB}); err != nil {
						t.Fatal(err)
					}
				},
				Config: config,
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{plancheck.ExpectResourceAction("pdns_tsig_key.k", plancheck.ResourceActionUpdate)},
				},
				Check: env.checkKey(name, "hmac-sha256", secretA),
			}),
			{
				// Deleted out of band: removed from state and re-created.
				PreConfig: func() {
					if err := env.client.DeleteTSIGKey(context.Background(), name+"."); err != nil {
						t.Fatal(err)
					}
				},
				Config: config,
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{plancheck.ExpectResourceAction("pdns_tsig_key.k", plancheck.ResourceActionCreate)},
				},
				Check: env.checkKey(name, "hmac-sha256", secretA),
			},
		},
	})
}
