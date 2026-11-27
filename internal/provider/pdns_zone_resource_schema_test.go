package provider

import (
	"context"
	"strings"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-go/tfprotov6"
	"github.com/hashicorp/terraform-plugin-go/tftypes"
)

// zoneProtocolServer returns the provider's protocol server and the
// Terraform type of the pdns_zone schema, for calling RPCs directly.
func zoneProtocolServer(t *testing.T) (tfprotov6.ProviderServer, tftypes.Type) {
	t.Helper()
	server, err := testProviderFactories["pdns"]()
	if err != nil {
		t.Fatal(err)
	}
	resp := &resource.SchemaResponse{}
	(&ZoneResource{}).Schema(context.Background(), resource.SchemaRequest{}, resp)
	return server, resp.Schema.Type().TerraformType(context.Background())
}

// TestZoneResourceAddressDeprecated checks that a configured address warns
// that it is deprecated, while addresses does not.
func TestZoneResourceAddressDeprecated(t *testing.T) {
	t.Parallel()
	server, _ := zoneProtocolServer(t)
	validate := func(nameserver string) []*tfprotov6.Diagnostic {
		t.Helper()
		resp, err := server.ValidateResourceConfig(context.Background(), &tfprotov6.ValidateResourceConfigRequest{
			TypeName: "pdns_zone",
			Config: &tfprotov6.DynamicValue{JSON: []byte(`{"name": "example.test.", "soa": {"rname": "hostmaster"}, ` +
				`"nameservers": [` + nameserver + `]}`)},
		})
		if err != nil {
			t.Fatal(err)
		}
		return resp.Diagnostics
	}

	diags := validate(`{"hostname": "ns1", "address": "10.0.0.1"}`)
	if len(diags) != 1 || diags[0].Severity != tfprotov6.DiagnosticSeverityWarning || !strings.Contains(diags[0].Detail, addressDeprecation) {
		t.Errorf("address: diagnostics = %+v, want one deprecation warning", diags)
	}
	if diags := validate(`{"hostname": "ns1", "addresses": ["10.0.0.1", "2001:db8::1"]}`); len(diags) != 0 {
		t.Errorf("addresses: diagnostics = %+v, want none", diags)
	}
}

// TestZoneResourceUpgradeStateWithoutAddresses reads state written by
// provider versions without addresses: the attribute is null, so no schema
// version or state upgrade is needed.
func TestZoneResourceUpgradeStateWithoutAddresses(t *testing.T) {
	t.Parallel()
	server, typ := zoneProtocolServer(t)
	resp, err := server.UpgradeResourceState(context.Background(), &tfprotov6.UpgradeResourceStateRequest{
		TypeName: "pdns_zone",
		Version:  0,
		RawState: &tfprotov6.RawState{JSON: []byte(`{
  "name": "example.test.", "serial": "2024010101", "dnssec": false, "kind": "Native", "ttl": 3600,
  "lock_dnsupdate": false, "masters": null,
  "nameservers": [{"hostname": "ns1", "address": "10.0.0.1"}, {"hostname": "ns2", "address": null}],
  "soa": {"rname": "hostmaster", "refresh": 86400, "retry": 7200, "expire": 4000000, "ttl": 11200, "create_record": true}
}`)},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(resp.Diagnostics) != 0 {
		t.Fatalf("diagnostics = %+v", resp.Diagnostics)
	}
	state, err := resp.UpgradedState.Unmarshal(typ)
	if err != nil {
		t.Fatal(err)
	}
	var attributes map[string]tftypes.Value
	var nameservers []tftypes.Value
	if err := state.As(&attributes); err != nil {
		t.Fatal(err)
	}
	if err := attributes["nameservers"].As(&nameservers); err != nil {
		t.Fatal(err)
	}
	for i, nameserver := range nameservers {
		var ns map[string]tftypes.Value
		if err := nameserver.As(&ns); err != nil {
			t.Fatal(err)
		}
		if !ns["addresses"].IsNull() {
			t.Errorf("nameservers[%d].addresses = %v, want null", i, ns["addresses"])
		}
		if i == 0 {
			var address string
			if err := ns["address"].As(&address); err != nil || address != "10.0.0.1" {
				t.Errorf("nameservers[0].address = %q (%v), want 10.0.0.1", address, err)
			}
		}
	}
}
