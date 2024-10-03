package provider

import (
	"context"
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
	Endpoint types.String `tfsdk:"endpoint"`
	APIKey   types.String `tfsdk:"api_key"`
	ServerID types.String `tfsdk:"server_id"`
}

func (p *PDNSProvider) Metadata(ctx context.Context, req provider.MetadataRequest, resp *provider.MetadataResponse) {
	resp.TypeName = "pdns"
	resp.Version = p.version
}

func (p *PDNSProvider) Schema(ctx context.Context, req provider.SchemaRequest, resp *provider.SchemaResponse) {
	resp.Schema = schema.Schema{
		Attributes: map[string]schema.Attribute{
			"endpoint": schema.StringAttribute{
				MarkdownDescription: "Example provider attribute",
				Optional:            false,
				Required:            true,
			},
			"server_id": schema.StringAttribute{
				MarkdownDescription: "Example provider attribute",
				Optional:            true,
			},
			"api_key": schema.StringAttribute{
				MarkdownDescription: "Example provider attribute",
				Optional:            false,
				Required:            true,
				Sensitive:           true,
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

	client := &http.Client{}

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
