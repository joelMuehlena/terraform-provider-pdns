package provider

// Acceptance tests run the resources against a real PowerDNS Authoritative
// server. They live in *_acc_test.go files, are named TestAcc*, and only run
// with TF_ACC=1 and the server configured through:
//
//	PDNS_ACC_ENDPOINT  base URL of the HTTP API, e.g. http://127.0.0.1:8081
//	PDNS_ACC_API_KEY   API key
//	PDNS_ACC_DNS_ADDR  optional host:port of the DNS listener, enables the
//	                   RFC2136 update checks, e.g. 127.0.0.1:15353
//
// Otherwise they are skipped. `mise run testacc` starts a server in a container
// (see test/acc) and runs them. Every test uses its own random zone and key
// names, so the tests run in parallel against one server.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"regexp"
	"slices"
	"strings"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/acctest"
	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/terraform"
	"github.com/samber/lo"
	"gitlab.com/joelMuehlena/homelab/code/terraform/provider/terraform-provider-pdns/internal/pdns_client"
)

// accEnv holds the connection settings of the acceptance test server.
type accEnv struct {
	endpoint string
	apiKey   string
	dnsAddr  string
	client   *pdns_client.PDNSClient
}

// testAccPreCheck skips the test unless acceptance tests are enabled and a
// server is configured, and returns a client for verifying server state.
func testAccPreCheck(t *testing.T) *accEnv {
	t.Helper()
	if os.Getenv("TF_ACC") == "" {
		t.Skip("acceptance tests are skipped unless TF_ACC is set")
	}
	env := &accEnv{
		endpoint: strings.TrimSuffix(os.Getenv("PDNS_ACC_ENDPOINT"), "/"),
		apiKey:   os.Getenv("PDNS_ACC_API_KEY"),
	}
	if env.endpoint == "" || env.apiKey == "" {
		t.Skip("acceptance tests need PDNS_ACC_ENDPOINT and PDNS_ACC_API_KEY")
	}
	env.client = pdns_client.NewPDNSClient(http.DefaultClient, env.endpoint, "localhost", env.apiKey)
	return env
}

// serverConfig returns the configuration settings of the server.
func (e *accEnv) serverConfig(t *testing.T) map[string]string {
	t.Helper()
	req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, e.endpoint+"/api/v1/servers/localhost/config", nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("X-API-Key", e.apiKey)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET config: status %d", resp.StatusCode)
	}
	var settings []struct{ Name, Value string }
	if err := json.NewDecoder(resp.Body).Decode(&settings); err != nil {
		t.Fatal(err)
	}
	return lo.SliceToMap(settings, func(s struct{ Name, Value string }) (string, string) { return s.Name, s.Value })
}

// tsigKeyUpdateFails reports whether the server cannot update an existing
// TSIG key in place. Before 5.1 the gpgsql backend stores keys with a plain
// INSERT (gpgsql-set-tsig-key-query), so PUT /tsigkeys/{id} of an existing
// key fails with a unique constraint violation and HTTP 500. 5.1 changed the
// query to an upsert.
func (e *accEnv) tsigKeyUpdateFails(t *testing.T) bool {
	t.Helper()
	config := e.serverConfig(t)
	if !strings.Contains(config["launch"], "gpgsql") {
		return false
	}
	return !strings.Contains(strings.ToLower(config["gpgsql-set-tsig-key-query"]), "on conflict")
}

var errTSIGKeyUpdate = regexp.MustCompile(`unexpected status code 500`)

// tsigKeyUpdateStep adjusts a step that updates a TSIG key in place: on
// servers where that fails (see tsigKeyUpdateFails) the step is expected to
// fail instead.
func (e *accEnv) tsigKeyUpdateStep(t *testing.T, step resource.TestStep) resource.TestStep {
	t.Helper()
	if e.tsigKeyUpdateFails(t) {
		t.Log("server cannot update TSIG keys in place (gpgsql before 5.1), expecting the update to fail")
		step.Check = nil
		step.ExpectError = errTSIGKeyUpdate
	}
	return step
}

func (e *accEnv) providerConfig() string {
	return fmt.Sprintf(`
provider "pdns" {
  endpoint = %q
  api_key  = %q
}
`, e.endpoint, e.apiKey)
}

// accRandom returns a random lowercase label unique to one test.
func accRandom() string {
	return "acc-" + strings.ToLower(acctest.RandString(10))
}

// zoneName returns a unique zone name and deletes the zone after the test
// in case Terraform did not.
func (e *accEnv) zoneName(t *testing.T, label string) string {
	t.Helper()
	name := label + "." + accRandom() + ".test."
	t.Cleanup(func() { _ = e.client.DeleteZone(context.Background(), name) })
	return name
}

// keyName returns a unique TSIG key name and deletes the key after the test
// in case Terraform did not.
func (e *accEnv) keyName(t *testing.T, label string) string {
	t.Helper()
	name := accRandom() + "-" + label
	t.Cleanup(func() { _ = e.client.DeleteTSIGKey(context.Background(), name+".") })
	return name
}

// createZone creates a bare zone through the API, for resources that need one.
func (e *accEnv) createZone(t *testing.T, label string) string {
	t.Helper()
	name := e.zoneName(t, label)
	_, err := e.client.CreateZone(context.Background(), pdns_client.PDNSZone{
		Name:        name,
		Kind:        "Native",
		Nameservers: []string{"ns1." + name},
	})
	if err != nil {
		t.Fatalf("create zone %s: %v", name, err)
	}
	return name
}

func (e *accEnv) zone(name string) (pdns_client.PDNSZone, error) {
	return e.client.GetZone(context.Background(), name, true, "")
}

var accSerialRe = regexp.MustCompile(`^(\S+ \S+ )\d+ `)

// checkRRsets asserts all rrsets of a zone on the server as
// "TYPE name content,..." lines with sorted contents. The SOA serial is
// replaced by SERIAL.
func (e *accEnv) checkRRsets(zoneName string, want ...string) func(*terraform.State) error {
	return func(*terraform.State) error {
		zone, err := e.zone(zoneName)
		if err != nil {
			return err
		}
		got := make([]string, 0, len(zone.Rrsets))
		for _, rr := range zone.Rrsets {
			contents := lo.Map(rr.Records, func(r pdns_client.Record, _ int) string {
				if rr.Type == "SOA" {
					return accSerialRe.ReplaceAllString(r.Content, "${1}SERIAL ")
				}
				return r.Content
			})
			slices.Sort(contents)
			got = append(got, rr.Type+" "+rr.Name+" "+strings.Join(contents, ","))
		}
		slices.Sort(got)
		slices.Sort(want)
		if !slices.Equal(got, want) {
			return fmt.Errorf("rrsets of %s =\n  %s\nwant\n  %s", zoneName, strings.Join(got, "\n  "), strings.Join(want, "\n  "))
		}
		return nil
	}
}

func (e *accEnv) checkZoneGone(names ...string) func(*terraform.State) error {
	return func(*terraform.State) error {
		for _, name := range names {
			_, err := e.zone(name)
			if _, ok := errors.AsType[*pdns_client.PDNSZoneNotFoundError](err); !ok {
				return fmt.Errorf("zone %s still exists (err=%v)", name, err)
			}
		}
		return nil
	}
}

func (e *accEnv) checkKey(name, algorithm, secret string) func(*terraform.State) error {
	return func(*terraform.State) error {
		key, err := e.client.GetTSIGKey(context.Background(), name+".")
		if err != nil {
			return err
		}
		if key.Algorithm != algorithm || key.Key != secret {
			return fmt.Errorf("server key %s = %s/%s, want %s/%s", name, key.Algorithm, key.Key, algorithm, secret)
		}
		return nil
	}
}

func (e *accEnv) checkKeyGone(names ...string) func(*terraform.State) error {
	return func(*terraform.State) error {
		for _, name := range names {
			_, err := e.client.GetTSIGKey(context.Background(), name+".")
			if _, ok := errors.AsType[*pdns_client.PDNSTSIGKeyNotFoundError](err); !ok {
				return fmt.Errorf("key %s still exists (err=%v)", name, err)
			}
		}
		return nil
	}
}
