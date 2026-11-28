package provider

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"regexp"
	"testing"
	"time"

	"github.com/hashicorp/terraform-plugin-framework/providerserver"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-go/tfprotov6"
	tfresource "github.com/hashicorp/terraform-plugin-testing/helper/resource"

	fwprovider "github.com/hashicorp/terraform-plugin-framework/provider"
	"gitlab.com/joelMuehlena/homelab/code/terraform/provider/terraform-provider-pdns/internal/version"
)

// testBuild is the build information of the provider in tests.
var testBuild = version.Info{Version: "test", Commit: "0123456789abcdef0123456789abcdef01234567", GoVersion: "go1.0", Platform: "plan9/386"}

// testProviderFactories serves the provider in-process to the Terraform CLI
// that terraform-plugin-testing drives.
var testProviderFactories = map[string]func() (tfprotov6.ProviderServer, error){
	"pdns": providerserver.NewProtocol6WithError(New(testBuild)()),
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
	p := New(version.Info{Version: "1.2.3"})()

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
	p := New(testBuild)()

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

	for _, newResource := range New(testBuild)().Resources(ctx) {
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

func TestNewHTTPClient(t *testing.T) {
	t.Parallel()

	for _, skip := range []bool{false, true} {
		client := newHTTPClient(skip, 42*time.Second)
		if client.Timeout != 42*time.Second {
			t.Errorf("timeout = %s, want 42s", client.Timeout)
		}
		transport, ok := client.Transport.(*http.Transport)
		if !ok {
			t.Fatalf("transport is %T", client.Transport)
		}
		if transport == http.DefaultTransport {
			t.Error("transport must be a clone, not http.DefaultTransport itself")
		}
		// Settings of http.DefaultTransport that a bare &http.Transport{} lacks.
		if transport.Proxy == nil {
			t.Error("transport ignores HTTP(S)_PROXY: Proxy is nil")
		}
		if transport.DialContext == nil || transport.TLSHandshakeTimeout == 0 || transport.IdleConnTimeout == 0 {
			t.Error("transport lacks the dial, TLS handshake or idle timeouts of http.DefaultTransport")
		}
		if transport.TLSClientConfig == nil || transport.TLSClientConfig.InsecureSkipVerify != skip {
			t.Errorf("TLS config = %+v, want InsecureSkipVerify %v", transport.TLSClientConfig, skip)
		}
	}
}

func TestParseTimeout(t *testing.T) {
	t.Parallel()

	for in, want := range map[string]time.Duration{"30s": 30 * time.Second, "1m30s": 90 * time.Second, "250ms": 250 * time.Millisecond, "0s": 0, "0": 0} {
		if got, err := parseTimeout(in); err != nil || got != want {
			t.Errorf("parseTimeout(%q) = %s, %v, want %s", in, got, err, want)
		}
	}
	for _, in := range []string{"", "30", "soon", "-1s"} {
		if _, err := parseTimeout(in); err == nil {
			t.Errorf("parseTimeout(%q) succeeded, want error", in)
		}
	}
}

func timeoutProviderConfig(endpoint, timeout string) string {
	return `
provider "pdns" {
  endpoint = "` + endpoint + `"
  api_key  = "` + fakeAPIKey + `"
  timeout  = "` + timeout + `"
}

resource "pdns_tsig_key" "k" {
  name = "k"
  key  = "YWFhYQ=="
}
`
}

func TestProviderTimeout(t *testing.T) {
	t.Parallel()

	// A server that does not answer in time.
	slow := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-r.Context().Done():
		case <-time.After(10 * time.Second):
		}
	}))
	t.Cleanup(slow.Close)

	start := time.Now()
	tfresource.UnitTest(t, tfresource.TestCase{
		ProtoV6ProviderFactories: testProviderFactories,
		Steps: []tfresource.TestStep{{
			Config:      timeoutProviderConfig(slow.URL, "200ms"),
			ExpectError: regexp.MustCompile(`Client.Timeout exceeded`),
		}},
	})
	if elapsed := time.Since(start); elapsed > 8*time.Second {
		t.Errorf("the request was not cut off by the timeout: took %s", elapsed)
	}
}

func TestProviderInvalidTimeout(t *testing.T) {
	t.Parallel()
	fake := newFakePDNS(t)

	for _, timeout := range []string{"soon", "30", "-1s"} {
		t.Run(timeout, func(t *testing.T) {
			t.Parallel()
			tfresource.UnitTest(t, tfresource.TestCase{
				ProtoV6ProviderFactories: testProviderFactories,
				Steps: []tfresource.TestStep{{
					Config:      timeoutProviderConfig(fake.srv.URL, timeout),
					ExpectError: regexp.MustCompile(`Invalid duration`),
				}},
			})
		})
	}
}

func serverIDConfig(endpoint, serverID string) string {
	return `
provider "pdns" {
  endpoint  = "` + endpoint + `"
  api_key   = "` + fakeAPIKey + `"
  server_id = "` + serverID + `"
}

resource "pdns_tsig_key" "k" {
  name = "k"
  key  = "YWFhYQ=="
}
`
}

// TestProviderWrongServerID checks that a wrong server_id is an error rather
// than an empty plan: PowerDNS answers requests to an unknown server with
// the same 404 as for a deleted object, which would drop every resource from
// the state (and make destroy a no-op).
func TestProviderWrongServerID(t *testing.T) {
	t.Parallel()
	fake := newFakePDNS(t)
	wrongServer := regexp.MustCompile(`server_id "wrong"\s+not\s+found`)

	tfresource.UnitTest(t, tfresource.TestCase{
		ProtoV6ProviderFactories: testProviderFactories,
		CheckDestroy:             checkFakeKeyGone(fake, "k"),
		Steps: []tfresource.TestStep{
			{
				// A trailing slash on the endpoint is ignored.
				Config: serverIDConfig(fake.srv.URL+"/", "localhost"),
				Check:  checkFakeKey(fake, "k", "hmac-sha256", "YWFhYQ=="),
			},
			{
				// Refreshing the existing key fails instead of removing it.
				Config:      serverIDConfig(fake.srv.URL, "wrong"),
				ExpectError: wrongServer,
			},
			{
				// The key is still in the state and on the server.
				Config:   serverIDConfig(fake.srv.URL, "localhost"),
				PlanOnly: true,
			},
		},
	})

	// Creating with a wrong server_id fails too.
	tfresource.UnitTest(t, tfresource.TestCase{
		ProtoV6ProviderFactories: testProviderFactories,
		Steps: []tfresource.TestStep{{
			Config:      serverIDConfig(newFakePDNS(t).srv.URL, "wrong"),
			ExpectError: wrongServer,
		}},
	})
}
