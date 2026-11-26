package provider

import (
	"fmt"
	"regexp"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/plancheck"
	"github.com/hashicorp/terraform-plugin-testing/terraform"
	"github.com/hashicorp/terraform-plugin-testing/tfversion"
)

const (
	secretA = "YWFhYWFhYWFhYWFhYWFhYWFhYWFhYWFhYWFhYWFhYWE="
	secretB = "YmJiYmJiYmJiYmJiYmJiYmJiYmJiYmJiYmJiYmJiYmI="
	secretC = "Y2NjY2NjY2NjY2NjY2NjY2NjY2NjY2NjY2NjY2NjY2M="
)

func tsigKeyConfig(fake *fakePDNS, body string) string {
	return fake.providerConfig() + `
resource "pdns_tsig_key" "k" {
  name = "tenant-a"
` + body + `
}
`
}

// checkFakeKey asserts the key as stored on the fake server.
func checkFakeKey(fake *fakePDNS, name, algorithm, secret string) resource.TestCheckFunc {
	return func(*terraform.State) error {
		key, ok := fake.key(name)
		if !ok {
			return fmt.Errorf("key %s does not exist on the server", name)
		}
		if key.Algorithm != algorithm || key.Key != secret {
			return fmt.Errorf("server key = %s/%s, want %s/%s", key.Algorithm, key.Key, algorithm, secret)
		}
		return nil
	}
}

func checkFakeKeyGone(fake *fakePDNS, names ...string) resource.TestCheckFunc {
	return func(*terraform.State) error {
		for _, name := range names {
			if _, ok := fake.key(name); ok {
				return fmt.Errorf("key %s still exists on the server", name)
			}
		}
		return nil
	}
}

func TestTSIGKeyResource(t *testing.T) {
	t.Parallel()
	fake := newFakePDNS(t)

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: testProviderFactories,
		CheckDestroy:             checkFakeKeyGone(fake, "tenant-a"),
		Steps: []resource.TestStep{
			{
				Config: tsigKeyConfig(fake, `key = "`+secretA+`"`),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("pdns_tsig_key.k", "id", "tenant-a."),
					resource.TestCheckResourceAttr("pdns_tsig_key.k", "name", "tenant-a"),
					resource.TestCheckResourceAttr("pdns_tsig_key.k", "algorithm", "hmac-sha256"),
					resource.TestCheckResourceAttr("pdns_tsig_key.k", "key", secretA),
					checkFakeKey(fake, "tenant-a", "hmac-sha256", secretA),
				),
			},
			{
				// A new secret updates the key in place.
				Config: tsigKeyConfig(fake, `key = "`+secretB+`"`),
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{plancheck.ExpectResourceAction("pdns_tsig_key.k", plancheck.ResourceActionUpdate)},
				},
				Check: checkFakeKey(fake, "tenant-a", "hmac-sha256", secretB),
			},
			{
				// A new algorithm replaces the key.
				Config: tsigKeyConfig(fake, `
  algorithm = "hmac-sha512"
  key       = "`+secretB+`"`),
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{plancheck.ExpectResourceAction("pdns_tsig_key.k", plancheck.ResourceActionDestroyBeforeCreate)},
				},
				Check: checkFakeKey(fake, "tenant-a", "hmac-sha512", secretB),
			},
			{
				ResourceName:      "pdns_tsig_key.k",
				ImportState:       true,
				ImportStateId:     "tenant-a.",
				ImportStateVerify: true,
			},
		},
	})
}

func TestTSIGKeyResourceWriteOnly(t *testing.T) {
	t.Parallel()
	fake := newFakePDNS(t)

	config := func(secret string, version int) string {
		return tsigKeyConfig(fake, fmt.Sprintf(`
  key_wo         = %q
  key_wo_version = %d`, secret, version))
	}

	resource.UnitTest(t, resource.TestCase{
		TerraformVersionChecks:   []tfversion.TerraformVersionCheck{tfversion.SkipBelow(tfversion.Version1_11_0)},
		ProtoV6ProviderFactories: testProviderFactories,
		CheckDestroy:             checkFakeKeyGone(fake, "tenant-a"),
		Steps: []resource.TestStep{
			{
				Config: config(secretA, 1),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckNoResourceAttr("pdns_tsig_key.k", "key"),
					resource.TestCheckNoResourceAttr("pdns_tsig_key.k", "key_wo"),
					resource.TestCheckResourceAttr("pdns_tsig_key.k", "key_wo_version", "1"),
					checkFakeKey(fake, "tenant-a", "hmac-sha256", secretA),
				),
			},
			{
				// A new secret without a version bump is not applied.
				Config: config(secretB, 1),
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{plancheck.ExpectEmptyPlan()},
				},
				Check: checkFakeKey(fake, "tenant-a", "hmac-sha256", secretA),
			},
			{
				// Bumping the version rotates the secret in place.
				Config: config(secretB, 2),
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{plancheck.ExpectResourceAction("pdns_tsig_key.k", plancheck.ResourceActionUpdate)},
				},
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckNoResourceAttr("pdns_tsig_key.k", "key"),
					checkFakeKey(fake, "tenant-a", "hmac-sha256", secretB),
				),
			},
			{
				// Switching to a key stored in state.
				Config: tsigKeyConfig(fake, `key = "`+secretC+`"`),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("pdns_tsig_key.k", "key", secretC),
					checkFakeKey(fake, "tenant-a", "hmac-sha256", secretC),
				),
			},
		},
	})
}

func TestTSIGKeyResourceDrift(t *testing.T) {
	t.Parallel()
	fake := newFakePDNS(t)
	config := tsigKeyConfig(fake, `key = "`+secretA+`"`)

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: testProviderFactories,
		Steps: []resource.TestStep{
			{Config: config},
			{
				// Changed out of band: restored in place.
				PreConfig: func() {
					fake.mutate(func(f *fakePDNS) { f.keys["tenant-a"].Key = secretB })
				},
				Config: config,
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{plancheck.ExpectResourceAction("pdns_tsig_key.k", plancheck.ResourceActionUpdate)},
				},
				Check: checkFakeKey(fake, "tenant-a", "hmac-sha256", secretA),
			},
			{
				// Deleted out of band: removed from state and re-created.
				PreConfig: func() {
					fake.mutate(func(f *fakePDNS) { delete(f.keys, "tenant-a") })
				},
				Config: config,
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{plancheck.ExpectResourceAction("pdns_tsig_key.k", plancheck.ResourceActionCreate)},
				},
				Check: checkFakeKey(fake, "tenant-a", "hmac-sha256", secretA),
			},
			{
				// Already deleted when destroying: no error.
				PreConfig: func() {
					fake.mutate(func(f *fakePDNS) { delete(f.keys, "tenant-a") })
				},
				Config:  fake.providerConfig(),
				Destroy: true,
			},
		},
	})
}

func TestTSIGKeyResourceValidation(t *testing.T) {
	t.Parallel()
	fake := newFakePDNS(t)

	tests := map[string]struct {
		body    string
		wantErr string
	}{
		"no secret":          {``, `Invalid Attribute Combination`},
		"both secrets":       {`key = "YQ=="` + "\n" + `key_wo = "YQ=="` + "\n" + `key_wo_version = 1`, `Invalid Attribute Combination`},
		"wo without version": {`key_wo = "YQ=="`, `Invalid Attribute Combination`},
		"version without wo": {`key = "YQ=="` + "\n" + `key_wo_version = 1`, `Invalid Attribute Combination`},
		"invalid base64":     {`key = "not base64!"`, `Invalid base64`},
		"invalid algorithm":  {`key = "YQ=="` + "\n" + `algorithm = "sha256"`, `Invalid Attribute Value Match`},
	}

	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			resource.UnitTest(t, resource.TestCase{
				TerraformVersionChecks:   []tfversion.TerraformVersionCheck{tfversion.SkipBelow(tfversion.Version1_11_0)},
				ProtoV6ProviderFactories: testProviderFactories,
				Steps: []resource.TestStep{{
					Config:      tsigKeyConfig(fake, tt.body),
					PlanOnly:    true,
					ExpectError: regexp.MustCompile(tt.wantErr),
				}},
			})
		})
	}
}

func TestTSIGKeyResourceServerError(t *testing.T) {
	t.Parallel()
	fake := newFakePDNS(t)
	// A key with this name already exists on the server.
	fake.addKey("tenant-a", secretB)

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: testProviderFactories,
		Steps: []resource.TestStep{{
			Config:      tsigKeyConfig(fake, `key = "`+secretA+`"`),
			ExpectError: regexp.MustCompile(`unexpected status code 409`),
		}},
	})
}

func TestTSIGKeyNameRejectsLockPlaceholder(t *testing.T) {
	t.Parallel()
	fake := newFakePDNS(t)

	for _, name := range []string{dnsupdateLockKey, "PDNS-provider-dnsupdate-locked"} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			resource.UnitTest(t, resource.TestCase{
				ProtoV6ProviderFactories: testProviderFactories,
				Steps: []resource.TestStep{{
					Config: fake.providerConfig() + `
resource "pdns_tsig_key" "k" {
  name = "` + name + `"
  key  = "` + secretA + `"
}
`,
					PlanOnly:    true,
					ExpectError: regexp.MustCompile(`Invalid Attribute Value Match`),
				}},
			})
		})
	}
}
