package provider

import (
	"context"
	"encoding/base64"
	"fmt"
	"strings"

	"github.com/hashicorp/terraform-plugin-framework-validators/int64validator"
	"github.com/hashicorp/terraform-plugin-framework-validators/stringvalidator"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringdefault"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"gitlab.com/joelMuehlena/homelab/code/terraform/provider/terraform-provider-pdns/internal/pdns_client"
)

var (
	_ resource.Resource                = &TSIGKeyResource{}
	_ resource.ResourceWithImportState = &TSIGKeyResource{}
	_ resource.ResourceWithConfigure   = &TSIGKeyResource{}
)

var tsigAlgorithms = []string{
	"hmac-md5",
	"hmac-sha1",
	"hmac-sha224",
	"hmac-sha256",
	"hmac-sha384",
	"hmac-sha512",
}

func NewTSIGKeyResource() resource.Resource {
	return &TSIGKeyResource{}
}

type TSIGKeyResource struct {
	providerData *PDNSProviderData
}

type TSIGKeyResourceModel struct {
	ID           types.String `tfsdk:"id"`
	Name         types.String `tfsdk:"name"`
	Algorithm    types.String `tfsdk:"algorithm"`
	Key          types.String `tfsdk:"key"`
	KeyWO        types.String `tfsdk:"key_wo"`
	KeyWOVersion types.Int64  `tfsdk:"key_wo_version"`
}

func (r *TSIGKeyResource) Metadata(ctx context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_tsig_key"
}

func (r *TSIGKeyResource) Schema(ctx context.Context, req resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "Manages a PowerDNS TSIG key. The secret is supplied either through `key` (stored in state) " +
			"or through the write-only `key_wo` (never stored in state or plan; requires Terraform >= 1.11 or OpenTofu >= 1.11). " +
			"Attach the key to zones with `pdns_zone_tsig_key`.\n\n" +
			"**Note:** with the `gpgsql` backend before PowerDNS 5.1, changing the secret of an existing key fails with HTTP 500, " +
			"because the default `gpgsql-set-tsig-key-query` is a plain `INSERT`. Use PowerDNS >= 5.1 or set the query to the 5.1 default: " +
			"`insert into tsigkeys (name,algorithm,secret) values($1,$2,$3) on conflict(name,algorithm) do update set secret=Excluded.secret`.",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				MarkdownDescription: "The PowerDNS ID of the TSIG key (usually the name with a trailing dot). Use this to attach the key to zones.",
				Computed:            true,
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.UseStateForUnknown(),
				},
			},
			"name": schema.StringAttribute{
				MarkdownDescription: "The name of the TSIG key. This is the key name clients (e.g. `nsupdate`, external-dns) must use. Changing it forces a new key.",
				Required:            true,
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.RequiresReplace(),
				},
				Validators: []validator.String{
					stringvalidator.LengthAtLeast(1),
				},
			},
			"algorithm": schema.StringAttribute{
				MarkdownDescription: fmt.Sprintf("The HMAC algorithm of the key. One of `%s`. Defaults to `hmac-sha256`. Changing it forces a new key.", strings.Join(tsigAlgorithms, "`, `")),
				Optional:            true,
				Computed:            true,
				Default:             stringdefault.StaticString("hmac-sha256"),
				// PowerDNS backends key TSIG rows on (name, algorithm): an in-place
				// algorithm change adds a second row instead of updating the key.
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.RequiresReplace(),
				},
				Validators: []validator.String{
					stringvalidator.OneOf(tsigAlgorithms...),
				},
			},
			"key": schema.StringAttribute{
				MarkdownDescription: "The base64 encoded secret of the key. Stored in state. Exactly one of `key` or `key_wo` must be set.",
				Optional:            true,
				Sensitive:           true,
				Validators: []validator.String{
					base64Validator{},
					stringvalidator.ExactlyOneOf(path.MatchRoot("key"), path.MatchRoot("key_wo")),
				},
			},
			"key_wo": schema.StringAttribute{
				MarkdownDescription: "The base64 encoded secret of the key as a write-only attribute: it is never stored in plan or state. " +
					"Must be used together with `key_wo_version`; bump the version to roll out a new secret. Exactly one of `key` or `key_wo` must be set.",
				Optional:  true,
				Sensitive: true,
				WriteOnly: true,
				Validators: []validator.String{
					base64Validator{},
					stringvalidator.AlsoRequires(path.MatchRoot("key_wo_version")),
				},
			},
			"key_wo_version": schema.Int64Attribute{
				MarkdownDescription: "Version of `key_wo`. As write-only values are not stored, changing this value is what triggers an update of the secret.",
				Optional:            true,
				Validators: []validator.Int64{
					int64validator.AlsoRequires(path.MatchRoot("key_wo")),
				},
			},
		},
	}
}

func (r *TSIGKeyResource) Configure(ctx context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
	if req.ProviderData == nil {
		return
	}

	providerData, ok := req.ProviderData.(*PDNSProviderData)
	if !ok {
		resp.Diagnostics.AddError("Parse Error", "Failed to parse provider data")
		return
	}

	r.providerData = providerData
}

func (r *TSIGKeyResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan, config TSIGKeyResourceModel

	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	resp.Diagnostics.Append(req.Config.Get(ctx, &config)...)
	if resp.Diagnostics.HasError() {
		return
	}

	// Write-only values are only available in the config, never in the plan.
	secret := plan.Key.ValueString()
	if plan.Key.IsNull() {
		secret = config.KeyWO.ValueString()
	}

	created, err := r.providerData.pdnsClient.CreateTSIGKey(ctx, pdns_client.TSIGKey{
		Name:      plan.Name.ValueString(),
		Algorithm: plan.Algorithm.ValueString(),
		Key:       secret,
	})
	if handleClientError(&resp.Diagnostics, err) {
		return
	}

	plan.ID = types.StringValue(created.ID)
	plan.KeyWO = types.StringNull()

	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *TSIGKeyResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var data TSIGKeyResourceModel

	resp.Diagnostics.Append(req.State.Get(ctx, &data)...)
	if resp.Diagnostics.HasError() {
		return
	}

	key, err := r.providerData.pdnsClient.GetTSIGKey(ctx, data.ID.ValueString())
	if isNotFound(err) {
		resp.Diagnostics.AddWarning(
			"TSIG key not found",
			fmt.Sprintf("The TSIG key %q no longer exists on the PowerDNS server and was removed from state. It will be re-created on the next apply.", data.ID.ValueString()),
		)
		resp.State.RemoveResource(ctx)
		return
	}
	if handleClientError(&resp.Diagnostics, err) {
		return
	}

	// Keep the configured spelling when it only differs by a trailing dot.
	if !sameDNSName(data.Name.ValueString(), key.Name) {
		data.Name = types.StringValue(key.Name)
	}
	data.ID = types.StringValue(key.ID)
	data.Algorithm = types.StringValue(strings.TrimSuffix(key.Algorithm, "."))
	data.KeyWO = types.StringNull()

	// Only track the secret in state when it is not managed write-only.
	if data.KeyWOVersion.IsNull() {
		data.Key = types.StringValue(key.Key)
	} else {
		data.Key = types.StringNull()
	}

	resp.Diagnostics.Append(resp.State.Set(ctx, &data)...)
}

func (r *TSIGKeyResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan, state, config TSIGKeyResourceModel

	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	resp.Diagnostics.Append(req.Config.Get(ctx, &config)...)
	if resp.Diagnostics.HasError() {
		return
	}

	// name and algorithm force replacement, so only the secret is updated in
	// place. The algorithm must not be sent: PowerDNS would store a second key
	// row for the new algorithm instead of changing the existing one.
	var secret string
	switch {
	case !plan.Key.IsNull() && !plan.Key.Equal(state.Key):
		secret = plan.Key.ValueString()
	case !plan.KeyWOVersion.IsNull() && !plan.KeyWOVersion.Equal(state.KeyWOVersion):
		secret = config.KeyWO.ValueString()
	}

	if secret != "" {
		_, err := r.providerData.pdnsClient.UpdateTSIGKey(ctx, state.ID.ValueString(), pdns_client.TSIGKey{Key: secret})
		if handleClientError(&resp.Diagnostics, err) {
			return
		}
	}

	plan.ID = state.ID
	plan.KeyWO = types.StringNull()

	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *TSIGKeyResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var data TSIGKeyResourceModel

	resp.Diagnostics.Append(req.State.Get(ctx, &data)...)
	if resp.Diagnostics.HasError() {
		return
	}

	err := r.providerData.pdnsClient.DeleteTSIGKey(ctx, data.ID.ValueString())
	if isNotFound(err) {
		return
	}
	handleClientError(&resp.Diagnostics, err)
}

func (r *TSIGKeyResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	resource.ImportStatePassthroughID(ctx, path.Root("id"), req, resp)
}

// base64Validator ensures a string is valid standard base64.
type base64Validator struct{}

func (v base64Validator) Description(ctx context.Context) string {
	return "value must be valid base64"
}

func (v base64Validator) MarkdownDescription(ctx context.Context) string {
	return v.Description(ctx)
}

func (v base64Validator) ValidateString(ctx context.Context, req validator.StringRequest, resp *validator.StringResponse) {
	if req.ConfigValue.IsNull() || req.ConfigValue.IsUnknown() {
		return
	}

	if _, err := base64.StdEncoding.DecodeString(req.ConfigValue.ValueString()); err != nil {
		resp.Diagnostics.AddAttributeError(req.Path, "Invalid base64", fmt.Sprintf("The value must be valid base64: %s", err))
	}
}
