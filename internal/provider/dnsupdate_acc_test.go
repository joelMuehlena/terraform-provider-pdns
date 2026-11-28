package provider

import (
	"context"
	"fmt"
	"os"
	"slices"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/terraform"
	"github.com/miekg/dns"
)

// dnsUpdate sends an RFC2136 update adding an A record to the zone, signed
// with the given TSIG key (hmac-sha256), or unsigned if keyName is empty, and
// returns the response rcode.
func dnsUpdate(addr, zone, keyName, secret, owner, ip string) (int, error) {
	rr, err := dns.NewRR(fmt.Sprintf("%s 60 IN A %s", owner, ip))
	if err != nil {
		return 0, err
	}
	msg := new(dns.Msg)
	msg.SetUpdate(zone)
	msg.Insert([]dns.RR{rr})
	client := &dns.Client{Net: "tcp"}
	if keyName != "" {
		msg.SetTsig(dns.Fqdn(keyName), dns.HmacSHA256, 300, 0)
		client.TsigSecret = map[string]string{dns.Fqdn(keyName): secret}
	}
	resp, _, err := client.Exchange(msg, addr)
	if err != nil {
		return 0, err
	}
	return resp.Rcode, nil
}

// checkDNSUpdate sends a signed update and asserts the outcome: accepted
// (NOERROR and the record exists on the server) or refused.
func (e *accEnv) checkDNSUpdate(zone, keyName, secret, owner string, accepted bool) resource.TestCheckFunc {
	return func(*terraform.State) error {
		rcode, err := dnsUpdate(e.dnsAddr, zone, keyName, secret, owner, "192.0.2.1")
		if err != nil {
			return fmt.Errorf("update signed by %s: %w", keyName, err)
		}
		if !accepted {
			if rcode == dns.RcodeSuccess {
				return fmt.Errorf("update of %s signed by %s was accepted, want refused", zone, keyName)
			}
			return e.checkRRset(zone, owner, "A", 0, nil, nil)(nil)
		}
		if rcode != dns.RcodeSuccess {
			return fmt.Errorf("update of %s signed by %s: rcode %s, want NOERROR", zone, keyName, dns.RcodeToString[rcode])
		}
		return e.checkRRset(zone, owner, "A", 60, []string{"192.0.2.1"}, nil)(nil)
	}
}

// TestAccZoneTSIGKeyDNSUpdate verifies end to end that dnsupdate = true lets
// the key sign RFC2136 updates of the zone, and only then. It needs the DNS
// listener of the server (PDNS_ACC_DNS_ADDR) running with dnsupdate=yes.
func TestAccZoneTSIGKeyDNSUpdate(t *testing.T) {
	env := testAccPreCheck(t)
	env.dnsAddr = os.Getenv("PDNS_ACC_DNS_ADDR")
	if env.dnsAddr == "" {
		t.Skip("PDNS_ACC_DNS_ADDR is not set")
	}
	zone := env.createZone(t, "dyn")
	updater, other := env.keyName(t, "updater"), env.keyName(t, "other")

	config := func(dnsupdate, otherDNSUpdate bool) string {
		return env.providerConfig() + fmt.Sprintf(`
resource "pdns_tsig_key" "updater" {
  name = %[2]q
  key  = %[4]q
}

resource "pdns_tsig_key" "other" {
  name = %[3]q
  key  = %[5]q
}

resource "pdns_zone_tsig_key" "updater" {
  zone        = %[1]q
  tsig_key_id = pdns_tsig_key.updater.id
  dnsupdate   = %[6]t
  axfr        = true
}

resource "pdns_zone_tsig_key" "other" {
  zone        = %[1]q
  tsig_key_id = pdns_tsig_key.other.id
  dnsupdate   = %[7]t
  axfr        = true
}
`, zone, updater, other, secretA, secretB, dnsupdate, otherDNSUpdate)
	}

	resource.ParallelTest(t, resource.TestCase{
		ProtoV6ProviderFactories: testProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: config(true, false),
				Check: resource.ComposeAggregateTestCheckFunc(
					env.checkDNSUpdate(zone, updater, secretA, "a."+zone, true),
					// Attached for axfr only.
					env.checkDNSUpdate(zone, other, secretB, "b."+zone, false),
				),
			},
			{
				Config: config(false, true),
				Check:  env.checkDNSUpdate(zone, updater, secretA, "c."+zone, false),
			},
		},
	})
}

// TestAccZoneDNSUpdateLock verifies end to end that a pdns_zone with
// lock_dnsupdate = true refuses unsigned updates whether or not keys are
// attached, and that switching it back to false (the default) re-allows them.
func TestAccZoneDNSUpdateLock(t *testing.T) {
	env := testAccPreCheck(t)
	env.dnsAddr = os.Getenv("PDNS_ACC_DNS_ADDR")
	if env.dnsAddr == "" {
		t.Skip("PDNS_ACC_DNS_ADDR is not set")
	}
	zone := env.zoneName(t, "lock")
	key := env.keyName(t, "lock")
	// With dnsupdate-require-tsig a zone without keys refuses unsigned
	// updates even when unlocked.
	unlockedAccepts := !env.requireTSIG(t)

	config := func(lock, attached bool) string {
		c := env.providerConfig() + fmt.Sprintf(`
resource "pdns_zone" "z" {
  name           = %[1]q
  lock_dnsupdate = %[4]t
  nameservers    = [{ hostname = "ns1" }]
  soa            = { rname = "hostmaster" }
}

resource "pdns_tsig_key" "k" {
  name = %[2]q
  key  = %[3]q
}
`, zone, key, secretA, lock)
		if attached {
			c += `
resource "pdns_zone_tsig_key" "k" {
  zone        = pdns_zone.z.name
  tsig_key_id = pdns_tsig_key.k.id
  dnsupdate   = true
}
`
		}
		return c
	}

	resource.ParallelTest(t, resource.TestCase{
		ProtoV6ProviderFactories: testProviderFactories,
		CheckDestroy:             env.checkZoneGone(zone),
		Steps: []resource.TestStep{
			{
				// Locked from creation, before any key is attached.
				Config: config(true, false),
				Check: resource.ComposeAggregateTestCheckFunc(
					env.checkDNSUpdateList(zone, dnsupdateLockKey),
					env.checkDNSUpdate(zone, "", "", "u1."+zone, false),
				),
			},
			{
				Config: config(true, true),
				Check: resource.ComposeAggregateTestCheckFunc(
					env.checkDNSUpdateList(zone, dnsupdateLockKey, key),
					env.checkDNSUpdate(zone, key, secretA, "s1."+zone, true),
					env.checkDNSUpdate(zone, "", "", "u2."+zone, false),
				),
			},
			{
				// The last key is removed: still locked.
				Config: config(true, false),
				Check: resource.ComposeAggregateTestCheckFunc(
					env.checkDNSUpdateList(zone, dnsupdateLockKey),
					env.checkDNSUpdate(zone, "", "", "u3."+zone, false),
					env.checkDNSUpdate(zone, key, secretA, "s2."+zone, false),
				),
			},
			{
				// Explicit opt-out: unsigned updates are accepted again (unless
				// the server requires TSIG).
				Config: config(false, false),
				Check: resource.ComposeAggregateTestCheckFunc(
					env.checkDNSUpdateList(zone),
					env.checkDNSUpdate(zone, "", "", "u4."+zone, unlockedAccepts),
				),
			},
		},
	})
}

// requireTSIG reports whether the server runs with dnsupdate-require-tsig=yes
// (PowerDNS 5.0+; 4.9 does not know the setting).
func (e *accEnv) requireTSIG(t *testing.T) bool {
	t.Helper()
	return e.serverConfig(t)["dnsupdate-require-tsig"] == "yes"
}

// TestAccDNSUpdateRequireTSIG verifies the behavior of the recommended
// server setting dnsupdate-require-tsig=yes that the docs describe: a zone
// without dnsupdate keys refuses unsigned updates and updates signed by any
// valid key, and once keys are attached only those keys are accepted. It
// also checks that lock_dnsupdate works alongside it. It only runs against a
// server started with the setting, e.g.
//
//	PDNS_ACC_SERVER_ARGS=--dnsupdate-require-tsig=yes mise run testacc
func TestAccDNSUpdateRequireTSIG(t *testing.T) {
	env := testAccPreCheck(t)
	env.dnsAddr = os.Getenv("PDNS_ACC_DNS_ADDR")
	if env.dnsAddr == "" {
		t.Skip("PDNS_ACC_DNS_ADDR is not set")
	}
	if !env.requireTSIG(t) {
		t.Skip("the server does not run with dnsupdate-require-tsig=yes")
	}
	zone := env.zoneName(t, "reqtsig")
	listed, other := env.keyName(t, "listed"), env.keyName(t, "other")

	config := func(lock, attached bool) string {
		c := env.providerConfig() + fmt.Sprintf(`
resource "pdns_zone" "z" {
  name           = %[1]q
  lock_dnsupdate = %[6]t
  nameservers    = [{ hostname = "ns1" }]
  soa            = { rname = "hostmaster" }
}

resource "pdns_tsig_key" "listed" {
  name = %[2]q
  key  = %[4]q
}

resource "pdns_tsig_key" "other" {
  name = %[3]q
  key  = %[5]q
}
`, zone, listed, other, secretA, secretB, lock)
		if attached {
			c += `
resource "pdns_zone_tsig_key" "listed" {
  zone        = pdns_zone.z.name
  tsig_key_id = pdns_tsig_key.listed.id
  dnsupdate   = true
}
`
		}
		return c
	}

	// Without keys (and unlocked) nothing is accepted.
	noKeys := resource.ComposeAggregateTestCheckFunc(
		env.checkDNSUpdate(zone, "", "", "u1."+zone, false),
		env.checkDNSUpdate(zone, listed, secretA, "l1."+zone, false),
		env.checkDNSUpdate(zone, other, secretB, "o1."+zone, false),
	)
	// Only the listed key is accepted, locked or not.
	listedOnly := func(step string) resource.TestCheckFunc {
		return resource.ComposeAggregateTestCheckFunc(
			env.checkDNSUpdate(zone, "", "", "u"+step+"."+zone, false),
			env.checkDNSUpdate(zone, listed, secretA, "l"+step+"."+zone, true),
			env.checkDNSUpdate(zone, other, secretB, "o"+step+"."+zone, false),
		)
	}

	resource.ParallelTest(t, resource.TestCase{
		ProtoV6ProviderFactories: testProviderFactories,
		CheckDestroy:             env.checkZoneGone(zone),
		Steps: []resource.TestStep{
			{Config: config(false, false), Check: noKeys},
			{Config: config(false, true), Check: listedOnly("2")},
			{Config: config(true, true), Check: listedOnly("3")},
			{
				// The last key is removed again: nothing is accepted.
				Config: config(false, false),
				Check: resource.ComposeAggregateTestCheckFunc(
					env.checkDNSUpdateList(zone),
					env.checkDNSUpdate(zone, "", "", "u4."+zone, false),
					env.checkDNSUpdate(zone, listed, secretA, "l4."+zone, false),
				),
			},
		},
	})
}

// TestAccZoneLockedCreate verifies that a zone created with lock_dnsupdate =
// true (created as Slave, locked, then switched to its kind) ends up with the
// same records as an unlocked one, including the A/AAAA glue of its
// nameservers, and with its configured kind.
func TestAccZoneLockedCreate(t *testing.T) {
	env := testAccPreCheck(t)
	env.dnsAddr = os.Getenv("PDNS_ACC_DNS_ADDR")
	name := env.zoneName(t, "locked")

	check := resource.ComposeAggregateTestCheckFunc(
		resource.TestCheckResourceAttr("pdns_zone.z", "kind", "Master"),
		resource.TestCheckResourceAttr("pdns_zone.z", "lock_dnsupdate", "true"),
		env.checkZoneSettings(name, "Master", false),
		env.checkRRsets(name,
			"SOA "+name+" ns1."+name+" hostmaster."+name+" SERIAL 86400 7200 4000000 11200",
			"NS "+name+" ns1."+name+",ns2."+name,
			"A ns1."+name+" 10.0.0.1",
			"AAAA ns2."+name+" 2001:db8::2",
		),
		env.checkDNSUpdateList(name, dnsupdateLockKey),
	)
	if env.dnsAddr != "" {
		check = resource.ComposeAggregateTestCheckFunc(check, env.checkDNSUpdate(name, "", "", "u."+name, false))
	}

	resource.ParallelTest(t, resource.TestCase{
		ProtoV6ProviderFactories: testProviderFactories,
		CheckDestroy:             env.checkZoneGone(name),
		Steps: []resource.TestStep{{
			Config: accZoneConfig(env, name, `
  kind           = "Master"
  lock_dnsupdate = true
  nameservers = [
    { hostname = "ns1", addresses = ["10.0.0.1"] },
    { hostname = "ns2", addresses = ["2001:db8::2"] },
  ]
  soa = { rname = "hostmaster" }`),
			Check: check,
		}},
	})
}

// checkDNSUpdateList asserts the zone's TSIG-ALLOW-DNSUPDATE on the server.
func (e *accEnv) checkDNSUpdateList(zone string, want ...string) resource.TestCheckFunc {
	return func(*terraform.State) error {
		names, err := e.client.GetZoneMetadata(context.Background(), zone, metadataTSIGAllowDNSUpdate)
		if err != nil {
			return err
		}
		if got, want := normalizedNames(names), normalizedNames(want); !slices.Equal(got, want) {
			return fmt.Errorf("zone %s dnsupdate list = %v, want %v", zone, got, want)
		}
		return nil
	}
}
