package provider

import (
	"context"
	"os"
	"regexp"
	"testing"
	"time"

	"github.com/hashicorp/terraform-plugin-framework/providerserver"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-go/tfprotov6"
	tfresource "github.com/hashicorp/terraform-plugin-testing/helper/resource"

	fwprovider "github.com/hashicorp/terraform-plugin-framework/provider"
)

// testProviderFactories serves the provider in-process to the Terraform CLI
// that terraform-plugin-testing drives.
var testProviderFactories = map[string]func() (tfprotov6.ProviderServer, error){
	"pdns": providerserver.NewProtocol6WithError(New("test")()),
}

func TestMain(m *testing.M) {
	// Keep the conflict retry of pdns_zone_tsig_key fast in tests that do
	// not exercise concurrent writers. Those use newTestAttachment instead.
	defaultApplyRetry = applyRetry{
		maxWrites: 8,
		settle:    10 * time.Millisecond,
		backoff:   func(attempt int) time.Duration { return time.Duration(attempt) * time.Millisecond },
	}

	os.Exit(m.Run())
}

func TestProviderMetadata(t *testing.T) {
	t.Parallel()
	p := New("1.2.3")()

	resp := &fwprovider.MetadataResponse{}
	p.Metadata(context.Background(), fwprovider.MetadataRequest{}, resp)
	if resp.TypeName != "pdns" || resp.Version != "1.2.3" {
		t.Errorf("metadata = %+v", resp)
	}

	pf := p.(fwprovider.ProviderWithFunctions)
	if len(pf.Functions(context.Background())) != 0 {
		t.Error("expected no functions")
	}
	if len(p.DataSources(context.Background())) != 0 {
		t.Error("expected no data sources")
	}
}

func TestSchemasAreValid(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	p := New("test")()

	presp := &fwprovider.SchemaResponse{}
	p.Schema(ctx, fwprovider.SchemaRequest{}, presp)
	if presp.Diagnostics.HasError() {
		t.Fatalf("provider schema: %v", presp.Diagnostics)
	}
	if diags := presp.Schema.ValidateImplementation(ctx); diags.HasError() {
		t.Fatalf("provider schema: %v", diags)
	}

	names := map[string]bool{}
	for _, newResource := range p.Resources(ctx) {
		r := newResource()

		mresp := &resource.MetadataResponse{}
		r.Metadata(ctx, resource.MetadataRequest{ProviderTypeName: "pdns"}, mresp)
		names[mresp.TypeName] = true

		sresp := &resource.SchemaResponse{}
		r.Schema(ctx, resource.SchemaRequest{}, sresp)
		if sresp.Diagnostics.HasError() {
			t.Errorf("%s schema: %v", mresp.TypeName, sresp.Diagnostics)
			continue
		}
		if diags := sresp.Schema.ValidateImplementation(ctx); diags.HasError() {
			t.Errorf("%s schema: %v", mresp.TypeName, diags)
		}
	}

	for _, want := range []string{"pdns_zone", "pdns_record", "pdns_tsig_key", "pdns_zone_tsig_key"} {
		if !names[want] {
			t.Errorf("resource %s is not registered", want)
		}
	}
}

func TestResourceConfigureRejectsWrongProviderData(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	for _, newResource := range New("test")().Resources(ctx) {
		r, ok := newResource().(resource.ResourceWithConfigure)
		if !ok {
			t.Fatalf("%T does not implement Configure", r)
		}

		// Unconfigured provider: no error, nothing set.
		resp := &resource.ConfigureResponse{}
		r.Configure(ctx, resource.ConfigureRequest{}, resp)
		if resp.Diagnostics.HasError() {
			t.Errorf("%T: unexpected error for nil provider data: %v", r, resp.Diagnostics)
		}

		resp = &resource.ConfigureResponse{}
		r.Configure(ctx, resource.ConfigureRequest{ProviderData: "wrong"}, resp)
		if !resp.Diagnostics.HasError() {
			t.Errorf("%T: expected error for wrong provider data", r)
		}
	}
}

func TestProviderWrongAPIKey(t *testing.T) {
	t.Parallel()
	fake := newFakePDNS(t)

	tfresource.UnitTest(t, tfresource.TestCase{
		ProtoV6ProviderFactories: testProviderFactories,
		Steps: []tfresource.TestStep{{
			Config: `
provider "pdns" {
  endpoint        = "` + fake.srv.URL + `"
  api_key         = "wrong"
  server_id       = "localhost"
  skip_tls_verify = true
}

resource "pdns_tsig_key" "k" {
  name = "k"
  key  = "YWFhYQ=="
}
`,
			ExpectError: regexp.MustCompile(`Authorization Error`),
		}},
	})
}
