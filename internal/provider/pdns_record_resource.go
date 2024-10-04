package provider

import (
	"context"
	"errors"
	"fmt"
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
		MarkdownDescription: "PowerDNS DNS Zone Resource",

		Attributes: map[string]schema.Attribute{
			"zone": schema.StringAttribute{
				Required:            true,
				MarkdownDescription: "ID of the zone in which the record should be created",
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.RequiresReplace(),
				},
			},
			"name": schema.StringAttribute{
				Required:            true,
				MarkdownDescription: "LValue of the record (name)",
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.RequiresReplace(),
				},
			},
			"type": schema.StringAttribute{
				Required:            true,
				MarkdownDescription: "Type of the record",
			},
			"ttl": schema.Int64Attribute{
				Optional:            true,
				MarkdownDescription: "TTL of the record",
				Computed:            true,
				Default:             int64default.StaticInt64(1800),
			},
			"comments": schema.ListAttribute{
				ElementType: types.StringType,
				Optional:    true,
			},
			"records": schema.ListAttribute{
				ElementType: types.StringType,
				Required:    true,
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

	err := r.providerData.pdnsClient.UpdateZoneRecords(ctx, data.Zone.ValueString(), []pdns_client.Rrset{{
		Type:       data.Type.ValueString(),
		TTL:        data.TTL.ValueInt64(),
		Changetype: "REPLACE",
		Name:       lo.Ternary(strings.HasSuffix(data.Name.ValueString(), "."), data.Name.ValueString(), data.Name.ValueString()+"."+data.Zone.ValueString()),
		Records: lo.Map(records, func(item string, index int) pdns_client.Record {
			return pdns_client.Record{
				Content:  item,
				Disabled: false,
			}
		}),
		Comments: lo.Map(comments, func(item string, index int) pdns_client.Comment {
			return pdns_client.Comment{
				Content: item,
			}
		}),
	}})

	var unauthorizedError *pdns_client.PDNSUnauthorizedError
	var notFoundError *pdns_client.PDNSZoneNotFoundError
	if err != nil && errors.As(err, &unauthorizedError) {
		resp.Diagnostics.AddError("Authorization Error", "Not authorized to access pdns api")
		return
	} else if err != nil && errors.As(err, &notFoundError) {
		resp.Diagnostics.AddError("Zone not found", notFoundError.Error())
		return
	} else if err != nil {
		resp.Diagnostics.AddError("Client Error", fmt.Sprintf("Unable to do http request to pdns API, got error: %s", err))
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

	expandedName := lo.Ternary(strings.HasSuffix(data.Name.ValueString(), "."), data.Name.ValueString(), data.Name.ValueString()+"."+data.Zone.ValueString())
	zone, err := r.providerData.pdnsClient.GetZone(ctx, data.Zone.ValueString(), true, expandedName)

	var unauthorizedError *pdns_client.PDNSUnauthorizedError
	var notFoundError *pdns_client.PDNSZoneNotFoundError
	if err != nil && errors.As(err, &unauthorizedError) {
		resp.Diagnostics.AddError("Authorization Error", "Not authorized to access pdns api")
		return
	} else if err != nil && errors.As(err, &notFoundError) {
		resp.Diagnostics.AddError("Zone not found", notFoundError.Error())
		return
	} else if err != nil {
		resp.Diagnostics.AddError("Client Error", fmt.Sprintf("Unable to do http request to pdns API, got error: %s", err))
		return
	}

	rrset, isFound := lo.Find(zone.Rrsets, func(item pdns_client.Rrset) bool {
		return item.Type == data.Type.ValueString()
	})

	if !isFound {
		resp.Diagnostics.AddError("Record not found", fmt.Sprintf("Failed to find a record with name '%s' and type '%s'", expandedName, data.Type.ValueString()))
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
		data.Comments = types.ListNull(types.StringType)
	} else {
		data.Comments = listValue
	}

	listValue, diags = types.ListValueFrom(ctx, types.StringType, lo.Map(rrset.Records, func(item pdns_client.Record, index int) attr.Value {
		return types.StringValue(item.Content)
	}))
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

	err := r.providerData.pdnsClient.UpdateZoneRecords(ctx, plan.Zone.ValueString(), []pdns_client.Rrset{{
		Type:       plan.Type.ValueString(),
		TTL:        plan.TTL.ValueInt64(),
		Changetype: "REPLACE",
		Name:       lo.Ternary(strings.HasSuffix(plan.Name.ValueString(), "."), plan.Name.ValueString(), plan.Name.ValueString()+"."+plan.Zone.ValueString()),
		Records: lo.Map(records, func(item string, index int) pdns_client.Record {
			return pdns_client.Record{
				Content:  item,
				Disabled: false,
			}
		}),
		Comments: lo.Map(comments, func(item string, index int) pdns_client.Comment {
			return pdns_client.Comment{
				Content: item,
			}
		}),
	}})

	var unauthorizedError *pdns_client.PDNSUnauthorizedError
	var notFoundError *pdns_client.PDNSZoneNotFoundError
	if err != nil && errors.As(err, &unauthorizedError) {
		resp.Diagnostics.AddError("Authorization Error", "Not authorized to access pdns api")
		return
	} else if err != nil && errors.As(err, &notFoundError) {
		resp.Diagnostics.AddError("Zone not found", notFoundError.Error())
		return
	} else if err != nil {
		resp.Diagnostics.AddError("Client Error", fmt.Sprintf("Unable to do http request to pdns API, got error: %s", err))
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

	err := r.providerData.pdnsClient.UpdateZoneRecords(ctx, data.Zone.ValueString(), []pdns_client.Rrset{{
		Type:       data.Type.ValueString(),
		Changetype: "DELETE",
		Name:       lo.Ternary(strings.HasSuffix(data.Name.ValueString(), "."), data.Name.ValueString(), data.Name.ValueString()+"."+data.Zone.ValueString()),
	}})

	var unauthorizedError *pdns_client.PDNSUnauthorizedError
	var notFoundError *pdns_client.PDNSZoneNotFoundError
	if err != nil && errors.As(err, &unauthorizedError) {
		resp.Diagnostics.AddError("Authorization Error", "Not authorized to access pdns api")
		return
	} else if err != nil && errors.As(err, &notFoundError) {
		resp.Diagnostics.AddError("Zone not found", notFoundError.Error())
		return
	} else if err != nil {
		resp.Diagnostics.AddError("Client Error", fmt.Sprintf("Unable to do http request to pdns API, got error: %s", err))
		return
	}
}

// TODO: Import record by name
func (r *RecordResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	resource.ImportStatePassthroughID(ctx, path.Root("name"), req, resp)
}
