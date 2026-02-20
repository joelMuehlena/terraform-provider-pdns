package provider

import (
	"context"
	"crypto/tls"
	"net/http"

	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/function"
	"github.com/hashicorp/terraform-plugin-framework/provider"
	"github.com/hashicorp/terraform-plugin-framework/provider/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"gitlab.com/joelMuehlena/homelab/code/terraform/provider/terraform-provider-pdns/internal/pdns_client"
)

var (
	_ provider.Provider              = &PDNSProvider{}
	_ provider.ProviderWithFunctions = &PDNSProvider{}
)

type PDNSProviderData struct {
	pdnsClient *pdns_client.PDNSClient
}

type PDNSProvider struct {
	version string
}

type PDNSProviderModel struct {
	Endpoint      types.String `tfsdk:"endpoint"`
	APIKey        types.String `tfsdk:"api_key"`
	ServerID      types.String `tfsdk:"server_id"`
	SkipTLSVerify types.Bool   `tfsdk:"skip_tls_verify"`
}

func (p *PDNSProvider) Metadata(ctx context.Context, req provider.MetadataRequest, resp *provider.MetadataResponse) {
	resp.TypeName = "pdns"
	resp.Version = p.version
}

func (p *PDNSProvider) Schema(ctx context.Context, req provider.SchemaRequest, resp *provider.SchemaResponse) {
	resp.Schema = schema.Schema{
		Attributes: map[string]schema.Attribute{
			"endpoint": schema.StringAttribute{
				MarkdownDescription: "API Endpoint of the the PowerDNS Auth API-Server",
				Optional:            false,
				Required:            true,
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
		},
	}
}

func (p *PDNSProvider) Configure(ctx context.Context, req provider.ConfigureRequest, resp *provider.ConfigureResponse) {
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

	client := &http.Client{
		Transport: &http.Transport{
			TLSClientConfig: &tls.Config{InsecureSkipVerify: data.SkipTLSVerify.ValueBool()},
		},
	}

	resp.ResourceData = &PDNSProviderData{
		pdnsClient: pdns_client.NewPDNSClient(
			client,
			data.Endpoint.ValueString(),
			data.ServerID.ValueString(),
			data.APIKey.ValueString(),
		),
	}
}

func (p *PDNSProvider) Resources(ctx context.Context) []func() resource.Resource {
	return []func() resource.Resource{
		NewZoneResource,
		NewRecordResource,
	}
}

func (p *PDNSProvider) DataSources(ctx context.Context) []func() datasource.DataSource {
	return []func() datasource.DataSource{}
}

func (p *PDNSProvider) Functions(ctx context.Context) []func() function.Function {
	return []func() function.Function{}
}

func New(version string) func() provider.Provider {
	return func() provider.Provider {
		return &PDNSProvider{
			version: version,
		}
	}
}
