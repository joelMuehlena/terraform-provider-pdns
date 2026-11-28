package provider

import (
	"context"
	"crypto/tls"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/function"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/provider"
	"github.com/hashicorp/terraform-plugin-framework/provider/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-log/tflog"
	"gitlab.com/joelMuehlena/homelab/code/terraform/provider/terraform-provider-pdns/internal/pdns_client"
	"gitlab.com/joelMuehlena/homelab/code/terraform/provider/terraform-provider-pdns/internal/version"
)

var (
	_ provider.Provider              = &PDNSProvider{}
	_ provider.ProviderWithFunctions = &PDNSProvider{}
)

type PDNSProviderData struct {
	pdnsClient *pdns_client.PDNSClient
	// zoneLocks serializes read-modify-write cycles on per-zone settings
	// (e.g. TSIG key lists) across resources applied in parallel.
	zoneLocks sync.Map
	// lockRetryConfig overrides defaultApplyRetry for setDNSUpdateLock; nil in
	// normal use.
	lockRetryConfig *applyRetry
}

// patchRecords changes rrsets of a zone. PATCH requests to the same zone are
// serialized: the gpgsql backend fails concurrent PATCHes of one zone with
// database deadlocks (HTTP 500 or 422), which Terraform's parallel record
// changes otherwise trigger. The client additionally retries those errors, for
// writers in other processes.
func (d *PDNSProviderData) patchRecords(ctx context.Context, zone string, rrsets []pdns_client.Rrset) error {
	unlock := d.lockZone(zone)
	defer unlock()
	return d.pdnsClient.UpdateZoneRecords(ctx, zone, rrsets)
}

// lockZone locks the given zone for exclusive modification and returns the
// unlock function.
func (d *PDNSProviderData) lockZone(zone string) func() {
	m, _ := d.zoneLocks.LoadOrStore(strings.ToLower(strings.TrimSuffix(zone, ".")), &sync.Mutex{})
	mu := m.(*sync.Mutex)
	mu.Lock()
	return mu.Unlock
}

type PDNSProvider struct {
	build version.Info
}

type PDNSProviderModel struct {
	Endpoint      types.String `tfsdk:"endpoint"`
	APIKey        types.String `tfsdk:"api_key"`
	ServerID      types.String `tfsdk:"server_id"`
	SkipTLSVerify types.Bool   `tfsdk:"skip_tls_verify"`
	Timeout       types.String `tfsdk:"timeout"`
}

// defaultTimeout bounds every request to the PowerDNS API unless the provider
// sets timeout.
const defaultTimeout = 30 * time.Second

func (p *PDNSProvider) Metadata(ctx context.Context, req provider.MetadataRequest, resp *provider.MetadataResponse) {
	resp.TypeName = "pdns"
	resp.Version = p.build.Version
}

func (p *PDNSProvider) Schema(ctx context.Context, req provider.SchemaRequest, resp *provider.SchemaResponse) {
	resp.Schema = schema.Schema{
		Attributes: map[string]schema.Attribute{
			"endpoint": schema.StringAttribute{
				MarkdownDescription: "API Endpoint of the the PowerDNS Auth API-Server: its base URL, without `/api/v1` (a trailing slash is ignored). " +
					"Before the first request the provider checks that `server_id` exists there and fails otherwise.",
				Optional: false,
				Required: true,
			},
			"server_id": schema.StringAttribute{
				MarkdownDescription: "Server id. If unset defaults to `localhost`. See [PowerDNS API docs](https://doc.powerdns.com/authoritative/http-api/server.html) for mor info",
				Optional:            true,
			},
			"api_key": schema.StringAttribute{
				MarkdownDescription: "API Key to authenticate against the PowerDNS server",
				Optional:            false,
				Required:            true,
				Sensitive:           true,
			},
			"skip_tls_verify": schema.BoolAttribute{
				MarkdownDescription: "Whether the verification of TLS certificates with the remote should be skipped.",
				Optional:            true,
				Required:            false,
			},
			"timeout": schema.StringAttribute{
				MarkdownDescription: "Timeout of each request to the PowerDNS API as a Go duration, e.g. `30s` or `2m`. " +
					"Covers connecting, sending the request and reading the whole response. `0s` disables the timeout. Defaults to `30s`.",
				Optional: true,
				Validators: []validator.String{
					durationValidator{},
				},
			},
		},
	}
}

// durationValidator accepts non-negative Go durations (time.ParseDuration).
type durationValidator struct{}

func (durationValidator) Description(context.Context) string {
	return "value must be a non-negative duration such as 30s or 2m"
}

func (v durationValidator) MarkdownDescription(ctx context.Context) string {
	return v.Description(ctx)
}

func (durationValidator) ValidateString(ctx context.Context, req validator.StringRequest, resp *validator.StringResponse) {
	if req.ConfigValue.IsNull() || req.ConfigValue.IsUnknown() {
		return
	}
	if _, err := parseTimeout(req.ConfigValue.ValueString()); err != nil {
		resp.Diagnostics.AddAttributeError(req.Path, "Invalid duration", err.Error())
	}
}

// parseTimeout parses the timeout attribute.
func parseTimeout(s string) (time.Duration, error) {
	d, err := time.ParseDuration(s)
	if err != nil {
		return 0, fmt.Errorf("%q is not a duration such as 30s or 2m: %w", s, err)
	}
	if d < 0 {
		return 0, fmt.Errorf("%q is negative", s)
	}
	return d, nil
}

// newHTTPClient returns the client for the PowerDNS API. It starts from
// http.DefaultTransport, which honors HTTP(S)_PROXY / NO_PROXY and has dial,
// TLS handshake and idle timeouts, and bounds every request by timeout
// (0 = no limit).
func newHTTPClient(skipTLSVerify bool, timeout time.Duration) *http.Client {
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.TLSClientConfig = &tls.Config{InsecureSkipVerify: skipTLSVerify}
	return &http.Client{Transport: transport, Timeout: timeout}
}

func (p *PDNSProvider) Configure(ctx context.Context, req provider.ConfigureRequest, resp *provider.ConfigureResponse) {
	p.logStart(ctx, req.TerraformVersion)

	var data PDNSProviderModel

	resp.Diagnostics.Append(req.Config.Get(ctx, &data)...)

	if resp.Diagnostics.HasError() {
		return
	}

	if data.ServerID.ValueString() == "" {
		data.ServerID = types.StringValue("localhost")
	}

	if data.SkipTLSVerify.IsNull() {
		data.SkipTLSVerify = types.BoolValue(false)
	}

	timeout := defaultTimeout
	if !data.Timeout.IsNull() && !data.Timeout.IsUnknown() {
		var err error
		if timeout, err = parseTimeout(data.Timeout.ValueString()); err != nil {
			resp.Diagnostics.AddAttributeError(path.Root("timeout"), "Invalid duration", err.Error())
			return
		}
	}

	client := newHTTPClient(data.SkipTLSVerify.ValueBool(), timeout)

	resp.ResourceData = &PDNSProviderData{
		pdnsClient: pdns_client.NewPDNSClient(
			client,
			data.Endpoint.ValueString(),
			data.ServerID.ValueString(),
			data.APIKey.ValueString(),
			// A wrong endpoint or server_id makes PowerDNS answer 404 like for
			// a deleted zone; the check turns that into an error instead of
			// emptying the state.
			pdns_client.WithServerCheck(),
			pdns_client.WithUserAgent(userAgent(p.build, req.TerraformVersion)),
		),
	}
}

func (p *PDNSProvider) Resources(ctx context.Context) []func() resource.Resource {
	return []func() resource.Resource{
		NewZoneResource,
		NewRecordResource,
		NewTSIGKeyResource,
		NewZoneTSIGKeyResource,
	}
}

func (p *PDNSProvider) DataSources(ctx context.Context) []func() datasource.DataSource {
	return []func() datasource.DataSource{}
}

func (p *PDNSProvider) Functions(ctx context.Context) []func() function.Function {
	return []func() function.Function{}
}

// logStart logs the build of the provider, so that a TF_LOG=INFO log tells
// which provider binary ran. It is called from Configure: Terraform calls it
// once in every provider process that talks to PowerDNS (each plan or apply,
// and each aliased provider configuration), and the framework only attaches
// the provider logger to the contexts of RPCs, not to the one passed to
// providerserver.Serve. Processes that only validate or read the schema do
// not log it.
func (p *PDNSProvider) logStart(ctx context.Context, terraformVersion string) {
	tflog.Info(ctx, "Starting PowerDNS provider", map[string]any{
		"version":           p.build.Version,
		"commit":            p.build.Commit,
		"commit_date":       p.build.CommitDate,
		"modified":          p.build.Modified,
		"go_version":        p.build.GoVersion,
		"platform":          p.build.Platform,
		"build":             p.build.String(),
		"terraform_version": terraformVersion,
	})
}

// userAgent returns the User-Agent of the requests to PowerDNS, e.g.
// "terraform-provider-pdns/1.2.3 (commit 0123456789ab) terraform/1.17.2",
// so that the PowerDNS or proxy logs tell which provider build and CLI
// version sent a request. terraformVersion is the version the CLI reports,
// for OpenTofu the OpenTofu version; it is left out if empty.
func userAgent(build version.Info, terraformVersion string) string {
	ua := fmt.Sprintf("terraform-provider-pdns/%s (commit %s)", build.Version, build.ShortCommit())
	if terraformVersion != "" {
		ua += " terraform/" + terraformVersion
	}
	return ua
}

// New returns the provider factory for the given build, version.Get() for
// the running binary.
func New(build version.Info) func() provider.Provider {
	return func() provider.Provider {
		return &PDNSProvider{
			build: build,
		}
	}
}
