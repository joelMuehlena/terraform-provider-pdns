package provider

import (
	"context"
	"fmt"
	"regexp"
	"slices"
	"strings"

	"github.com/hashicorp/terraform-plugin-framework-validators/listvalidator"
	"github.com/hashicorp/terraform-plugin-framework-validators/stringvalidator"
	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/int64default"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-log/tflog"
	"github.com/samber/lo"
	"gitlab.com/joelMuehlena/homelab/code/terraform/provider/terraform-provider-pdns/internal/pdns_client"
)

var (
	_ resource.Resource                = &RecordResource{}
	_ resource.ResourceWithImportState = &RecordResource{}
	_ resource.ResourceWithConfigure   = &RecordResource{}
)

func NewRecordResource() resource.Resource {
	return &RecordResource{}
}

type RecordResource struct {
	providerData *PDNSProviderData
}

type RecordResourceModel struct {
	Comments types.List   `tfsdk:"comments"`
	Records  types.List   `tfsdk:"records"`
	Zone     types.String `tfsdk:"zone"`
	Type     types.String `tfsdk:"type"`
	Name     types.String `tfsdk:"name"`
	TTL      types.Int64  `tfsdk:"ttl"`
}

func (r *RecordResource) Metadata(ctx context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_record"
}

func (r *RecordResource) Schema(ctx context.Context, req resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "Manages a single DNS record (rrset) within a PowerDNS zone.",

		Attributes: map[string]schema.Attribute{
			"zone": schema.StringAttribute{
				Required:            true,
				MarkdownDescription: "ID of the zone in which the record should be created. The name must end with a dot `.`.",
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.RequiresReplace(),
				},
				Validators: []validator.String{
					stringvalidator.RegexMatches(regexp.MustCompile(`\.$`), "Name must end with a dot"),
				},
			},
			"name": schema.StringAttribute{
				Required:            true,
				MarkdownDescription: "LValue of the record (name). This value will be added as prefix to the name. Supports chaining by dot e.g. `sub.test` is a valid value.",
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.RequiresReplace(),
				},
			},
			"type": schema.StringAttribute{
				Required:            true,
				MarkdownDescription: "Type of the record in uppercase, e.g. `A`, `AAAA` or `CNAME`. Changing it replaces the record.",
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.RequiresReplace(),
				},
				Validators: []validator.String{
					// PowerDNS stores types in uppercase; a lowercase type
					// would never be found again on read.
					stringvalidator.RegexMatches(
						regexp.MustCompile(`^[A-Z][A-Z0-9]*$`),
						"must be an uppercase record type such as A, AAAA or CNAME (PowerDNS stores record types in uppercase)",
					),
				},
			},
			"ttl": schema.Int64Attribute{
				Optional:            true,
				MarkdownDescription: "TTL of the record",
				Computed:            true,
				Default:             int64default.StaticInt64(1800),
			},
			"comments": schema.ListAttribute{
				ElementType:         types.StringType,
				MarkdownDescription: "List of comments to append to the record",
				Optional:            true,
			},
			"records": schema.ListAttribute{
				ElementType: types.StringType,
				Required:    true,
				MarkdownDescription: "RValue to which the record points. For A type records this are IP Addresses. For CNAMEs this are other FQDNs and so on. " +
					"The order is not significant: PowerDNS returns the records sorted and in its canonical form (e.g. AAAA addresses compressed and lowercase), " +
					"and the configured values are kept as long as the server holds the same records.",
				Validators: []validator.List{
					listvalidator.SizeAtLeast(1),
					listvalidator.UniqueValues(),
					listvalidator.ValueStringsAre(stringvalidator.LengthAtLeast(1)),
				},
			},
		},
	}
}

func (r *RecordResource) Configure(ctx context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
	// Prevent panic if the provider has not been configured.
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

func (r *RecordResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var data RecordResourceModel

	resp.Diagnostics.Append(req.Plan.Get(ctx, &data)...)

	if resp.Diagnostics.HasError() {
		return
	}

	records := make([]string, 0, len(data.Records.Elements()))
	diags := data.Records.ElementsAs(ctx, &records, false)
	if diags.HasError() {
		resp.Diagnostics = append(resp.Diagnostics, diags...)
		return
	}

	var comments []string
	if !data.Comments.IsNull() {
		comments = make([]string, 0, len(data.Comments.Elements()))
		diags := data.Comments.ElementsAs(ctx, &comments, false)
		if diags.HasError() {
			resp.Diagnostics = append(resp.Diagnostics, diags...)
			return
		}
	} else {
		comments = make([]string, 0)
	}

	err := r.providerData.patchRecords(ctx, data.Zone.ValueString(), []pdns_client.Rrset{{
		Type:       data.Type.ValueString(),
		TTL:        data.TTL.ValueInt64(),
		Changetype: "REPLACE",
		Name:       fqdn(data.Name.ValueString(), data.Zone.ValueString()),
		Records: lo.Map(records, func(item string, index int) pdns_client.Record {
			return pdns_client.Record{
				Content:  item,
				Disabled: false,
			}
		}),
		Comments: recordComments(comments),
	}})
	if handleClientError(&resp.Diagnostics, err) {
		return
	}

	resp.Diagnostics.Append(resp.State.Set(ctx, &data)...)
}

func (r *RecordResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var data RecordResourceModel

	// Read Terraform prior state data into the model
	resp.Diagnostics.Append(req.State.Get(ctx, &data)...)

	if resp.Diagnostics.HasError() {
		return
	}

	expandedName := fqdn(data.Name.ValueString(), data.Zone.ValueString())
	zone, err := r.providerData.pdnsClient.GetZone(ctx, data.Zone.ValueString(), true, expandedName)
	if isNotFound(err) {
		resp.Diagnostics.AddWarning(
			"Zone not found",
			fmt.Sprintf("The zone %q of record %q no longer exists on the PowerDNS server, so the record was removed from state.", data.Zone.ValueString(), expandedName),
		)
		resp.State.RemoveResource(ctx)
		return
	}
	if handleClientError(&resp.Diagnostics, err) {
		return
	}

	rrset, isFound := lo.Find(zone.Rrsets, func(item pdns_client.Rrset) bool {
		return item.Type == data.Type.ValueString()
	})

	if !isFound {
		// Deleted out of band: drop it from state so that it is re-created.
		tflog.Warn(ctx, "Record not found, removing it from state", map[string]any{"name": expandedName, "type": data.Type.ValueString()})
		resp.State.RemoveResource(ctx)
		return
	}

	data.TTL = types.Int64Value(rrset.TTL)
	data.Type = types.StringValue(rrset.Type)

	listValue, diags := types.ListValueFrom(ctx, types.StringType, lo.Map(rrset.Comments, func(item pdns_client.Comment, index int) attr.Value {
		return types.StringValue(item.Content)
	}))
	if diags.HasError() {
		resp.Diagnostics = append(resp.Diagnostics, diags...)
		return
	}

	if len(listValue.Elements()) == 0 {
		// No comments: keep an empty list from the configuration (comments =
		// []) as is, so that it does not differ from null forever.
		if data.Comments.IsUnknown() || len(data.Comments.Elements()) > 0 {
			data.Comments = types.ListNull(types.StringType)
		}
	} else {
		data.Comments = listValue
	}

	var prior []string
	if !data.Records.IsNull() && !data.Records.IsUnknown() {
		resp.Diagnostics.Append(data.Records.ElementsAs(ctx, &prior, false)...)
		if resp.Diagnostics.HasError() {
			return
		}
	}
	server := lo.Map(rrset.Records, func(item pdns_client.Record, _ int) string { return item.Content })

	listValue, diags = types.ListValueFrom(ctx, types.StringType, reconcileRecords(rrset.Type, prior, server))
	if diags.HasError() {
		resp.Diagnostics = append(resp.Diagnostics, diags...)
		return
	}

	data.Records = listValue

	resp.Diagnostics.Append(resp.State.Set(ctx, &data)...)
}

func (r *RecordResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan, state RecordResourceModel

	// Read Terraform plan data into the model
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)

	if resp.Diagnostics.HasError() {
		return
	}

	records := make([]string, 0, len(plan.Records.Elements()))
	diags := plan.Records.ElementsAs(ctx, &records, false)
	if diags.HasError() {
		resp.Diagnostics = append(resp.Diagnostics, diags...)
		return
	}

	var comments []string
	if !plan.Comments.IsNull() {
		comments = make([]string, 0, len(plan.Comments.Elements()))
		diags := plan.Comments.ElementsAs(ctx, &comments, false)
		if diags.HasError() {
			resp.Diagnostics = append(resp.Diagnostics, diags...)
			return
		}
	} else {
		comments = make([]string, 0)
	}

	err := r.providerData.patchRecords(ctx, plan.Zone.ValueString(), []pdns_client.Rrset{{
		Type:       plan.Type.ValueString(),
		TTL:        plan.TTL.ValueInt64(),
		Changetype: "REPLACE",
		Name:       fqdn(plan.Name.ValueString(), plan.Zone.ValueString()),
		Records: lo.Map(records, func(item string, index int) pdns_client.Record {
			return pdns_client.Record{
				Content:  item,
				Disabled: false,
			}
		}),
		Comments: recordComments(comments),
	}})
	if handleClientError(&resp.Diagnostics, err) {
		return
	}

	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *RecordResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var data RecordResourceModel

	resp.Diagnostics.Append(req.State.Get(ctx, &data)...)

	if resp.Diagnostics.HasError() {
		return
	}

	err := r.providerData.patchRecords(ctx, data.Zone.ValueString(), []pdns_client.Rrset{{
		Type:       data.Type.ValueString(),
		Changetype: "DELETE",
		Name:       fqdn(data.Name.ValueString(), data.Zone.ValueString()),
	}})
	if isNotFound(err) {
		// The zone is gone, and with it the record.
		return
	}
	handleClientError(&resp.Diagnostics, err)
}

// recordComments returns the comments to send with a REPLACE of the rrset. It
// is never nil: the client omits nil comments, and PowerDNS then keeps the
// comments on the server, so removing comments from the configuration would
// never take effect.
func recordComments(comments []string) []pdns_client.Comment {
	out := make([]pdns_client.Comment, 0, len(comments))
	for _, c := range comments {
		out = append(out, pdns_client.Comment{Content: c})
	}
	return out
}

// ImportState imports a record by an id of the form <zone>:<name>:<type>.
// name is taken as given, relative to the zone or fully qualified, so it must
// be written as in the configuration; zone gets a trailing dot if it lacks one.
func (r *RecordResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	// ":" separates the parts: "/" is valid in RFC 2317 classless reverse
	// zones (e.g. 0/26.2.0.192.in-addr.arpa.) and their record names.
	parts := strings.Split(req.ID, ":")
	if len(parts) != 3 || slices.Contains(parts, "") {
		resp.Diagnostics.AddError(
			"Invalid import ID",
			fmt.Sprintf("Expected an import ID of the form <zone>:<name>:<type>, e.g. example.com.:www:A, got %q.", req.ID),
		)
		return
	}

	zone, name, typ := parts[0], parts[1], strings.ToUpper(parts[2])
	if !strings.HasSuffix(zone, ".") {
		zone += "."
	}

	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("zone"), zone)...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("name"), name)...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("type"), typ)...)
}
