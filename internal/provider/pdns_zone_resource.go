package provider

import (
	"context"
	"errors"
	"fmt"
	"net"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/hashicorp/terraform-plugin-framework-validators/listvalidator"
	"github.com/hashicorp/terraform-plugin-framework-validators/stringvalidator"
	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/booldefault"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/int64default"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringdefault"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-framework/types/basetypes"
	"github.com/hashicorp/terraform-plugin-log/tflog"
	"github.com/samber/lo"
	"gitlab.com/joelMuehlena/homelab/code/terraform/provider/terraform-provider-pdns/internal/pdns_client"
)

var (
	_ resource.Resource                = &ZoneResource{}
	_ resource.ResourceWithImportState = &ZoneResource{}
	_ resource.ResourceWithConfigure   = &ZoneResource{}
)

func NewZoneResource() resource.Resource {
	return &ZoneResource{}
}

type ZoneResource struct {
	providerData *PDNSProviderData
}

type ZoneResourceModel struct {
	Nameservers types.List   `tfsdk:"nameservers"`
	Masters     types.List   `tfsdk:"masters"`
	Name        types.String `tfsdk:"name"`
	Kind        types.String `tfsdk:"kind"`
	SOA         types.Object `tfsdk:"soa"`
	DNSSec      types.Bool   `tfsdk:"dnssec"`
}

type Nameserver struct {
	Address      string `tfsdk:"address"`
	Hostname     string `tfsdk:"hostname"`
	CreateRecord bool   `tfsdk:"create_record"`
}

type SOA struct {
	RName        string `tfsdk:"rname"`
	Refresh      int64  `tfsdk:"refresh"`
	Retry        int64  `tfsdk:"retry"`
	Expire       int64  `tfsdk:"expire"`
	TTL          int64  `tfsdk:"ttl"`
	CreateRecord bool   `tfsdk:"create_record"`
}

type SOAModel struct {
	RName        types.String `tfsdk:"rname"`
	Refresh      types.Int64  `tfsdk:"refresh"`
	Retry        types.Int64  `tfsdk:"retry"`
	Expire       types.Int64  `tfsdk:"expire"`
	TTL          types.Int64  `tfsdk:"ttl"`
	CreateRecord types.Bool   `tfsdk:"create_record"`
}

func SOAToSOAModel(s SOA) SOAModel {
	return SOAModel{
		RName:        types.StringValue(s.RName),
		Refresh:      types.Int64Value(s.Refresh),
		Retry:        types.Int64Value(s.Retry),
		Expire:       types.Int64Value(s.Expire),
		TTL:          types.Int64Value(s.TTL),
		CreateRecord: types.BoolValue(s.CreateRecord),
	}
}

func (m SOAModel) AttributeTypes() map[string]attr.Type {
	return map[string]attr.Type{
		"rname":         types.StringType,
		"refresh":       types.Int64Type,
		"retry":         types.Int64Type,
		"expire":        types.Int64Type,
		"ttl":           types.Int64Type,
		"create_record": types.BoolType,
	}
}

func (r *ZoneResource) Metadata(ctx context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_zone"
}

var IP_REGEX = regexp.MustCompile(`^((([0-9]|[1-9][0-9]|1[0-9]{2}|2[0-4][0-9]|25[0-5])\.){3}([0-9]|[1-9][0-9]|1[0-9]{2}|2[0-4][0-9]|25[0-5])$|^(([a-fA-F]|[a-fA-F][a-fA-F0-9\-]*[a-fA-F0-9])\.)*([A-Fa-f]|[A-Fa-f][A-Fa-f0-9\-]*[A-Fa-f0-9])$|^(?:(?:(?:(?:(?:(?:(?:[0-9a-fA-F]{1,4})):){6})(?:(?:(?:(?:(?:[0-9a-fA-F]{1,4})):(?:(?:[0-9a-fA-F]{1,4})))|(?:(?:(?:(?:(?:25[0-5]|(?:[1-9]|1[0-9]|2[0-4])?[0-9]))\.){3}(?:(?:25[0-5]|(?:[1-9]|1[0-9]|2[0-4])?[0-9])))))))|(?:(?:::(?:(?:(?:[0-9a-fA-F]{1,4})):){5})(?:(?:(?:(?:(?:[0-9a-fA-F]{1,4})):(?:(?:[0-9a-fA-F]{1,4})))|(?:(?:(?:(?:(?:25[0-5]|(?:[1-9]|1[0-9]|2[0-4])?[0-9]))\.){3}(?:(?:25[0-5]|(?:[1-9]|1[0-9]|2[0-4])?[0-9])))))))|(?:(?:(?:(?:(?:[0-9a-fA-F]{1,4})))?::(?:(?:(?:[0-9a-fA-F]{1,4})):){4})(?:(?:(?:(?:(?:[0-9a-fA-F]{1,4})):(?:(?:[0-9a-fA-F]{1,4})))|(?:(?:(?:(?:(?:25[0-5]|(?:[1-9]|1[0-9]|2[0-4])?[0-9]))\.){3}(?:(?:25[0-5]|(?:[1-9]|1[0-9]|2[0-4])?[0-9])))))))|(?:(?:(?:(?:(?:(?:[0-9a-fA-F]{1,4})):){0,1}(?:(?:[0-9a-fA-F]{1,4})))?::(?:(?:(?:[0-9a-fA-F]{1,4})):){3})(?:(?:(?:(?:(?:[0-9a-fA-F]{1,4})):(?:(?:[0-9a-fA-F]{1,4})))|(?:(?:(?:(?:(?:25[0-5]|(?:[1-9]|1[0-9]|2[0-4])?[0-9]))\.){3}(?:(?:25[0-5]|(?:[1-9]|1[0-9]|2[0-4])?[0-9])))))))|(?:(?:(?:(?:(?:(?:[0-9a-fA-F]{1,4})):){0,2}(?:(?:[0-9a-fA-F]{1,4})))?::(?:(?:(?:[0-9a-fA-F]{1,4})):){2})(?:(?:(?:(?:(?:[0-9a-fA-F]{1,4})):(?:(?:[0-9a-fA-F]{1,4})))|(?:(?:(?:(?:(?:25[0-5]|(?:[1-9]|1[0-9]|2[0-4])?[0-9]))\.){3}(?:(?:25[0-5]|(?:[1-9]|1[0-9]|2[0-4])?[0-9])))))))|(?:(?:(?:(?:(?:(?:[0-9a-fA-F]{1,4})):){0,3}(?:(?:[0-9a-fA-F]{1,4})))?::(?:(?:[0-9a-fA-F]{1,4})):)(?:(?:(?:(?:(?:[0-9a-fA-F]{1,4})):(?:(?:[0-9a-fA-F]{1,4})))|(?:(?:(?:(?:(?:25[0-5]|(?:[1-9]|1[0-9]|2[0-4])?[0-9]))\.){3}(?:(?:25[0-5]|(?:[1-9]|1[0-9]|2[0-4])?[0-9])))))))|(?:(?:(?:(?:(?:(?:[0-9a-fA-F]{1,4})):){0,4}(?:(?:[0-9a-fA-F]{1,4})))?::)(?:(?:(?:(?:(?:[0-9a-fA-F]{1,4})):(?:(?:[0-9a-fA-F]{1,4})))|(?:(?:(?:(?:(?:25[0-5]|(?:[1-9]|1[0-9]|2[0-4])?[0-9]))\.){3}(?:(?:25[0-5]|(?:[1-9]|1[0-9]|2[0-4])?[0-9])))))))|(?:(?:(?:(?:(?:(?:[0-9a-fA-F]{1,4})):){0,5}(?:(?:[0-9a-fA-F]{1,4})))?::)(?:(?:[0-9a-fA-F]{1,4})))|(?:(?:(?:(?:(?:(?:[0-9a-fA-F]{1,4})):){0,6}(?:(?:[0-9a-fA-F]{1,4})))?::)))))$`)

// TODO: Allow for TSIG
// TODO: Further DNSSec functionality?
// TODO: Support for other kinds Like slave or master

func (r *ZoneResource) Schema(ctx context.Context, req resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "PowerDNS DNS Zone Resource",

		Attributes: map[string]schema.Attribute{
			"name": schema.StringAttribute{
				MarkdownDescription: "The Name of the zone to be created",
				Required:            true,
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.RequiresReplace(),
				},
				Validators: []validator.String{
					stringvalidator.RegexMatches(regexp.MustCompile(`\.$`), "Name must end with a dot"),
				},
			},
			"dnssec": schema.BoolAttribute{
				MarkdownDescription: "Whether or not this zone is DNSSEC signed",
				Optional:            true,
				Default:             booldefault.StaticBool(false),
				Computed:            true,
			},
			"nameservers": schema.ListNestedAttribute{
				MarkdownDescription: "The nameservers of the Zone",
				Required:            true,
				Validators: []validator.List{
					listvalidator.SizeAtLeast(1),
				},
				NestedObject: schema.NestedAttributeObject{
					Attributes: map[string]schema.Attribute{
						"hostname": schema.StringAttribute{
							Required:    true,
							Description: "The hostname of the nameservers. Will be prefixed with the zone name if not ending with an explicit '.'",
						},
						"create_record": schema.BoolAttribute{
							Optional:            true,
							Default:             booldefault.StaticBool(true),
							Computed:            true,
							MarkdownDescription: "If set to false no A record for the name server will be created",
						},
						"address": schema.StringAttribute{
							Required:    true,
							Description: "The IP Address of the nameserver",
							Validators: []validator.String{
								stringvalidator.RegexMatches(IP_REGEX, "The passed string is not a valid IPv4 or valid IPv6 Address"),
							},
						},
					},
				},
			},
			"kind": schema.StringAttribute{
				MarkdownDescription: "",
				Optional:            true,
				Default:             stringdefault.StaticString("Native"),
				Computed:            true,
				Validators: []validator.String{
					stringvalidator.OneOf("Native", "Master", "Slave", "Producer", "Consumer"),
				},
			},
			"masters": schema.ListAttribute{
				MarkdownDescription: "Masters of this zone should onlt be set if kind is Slave",
				Optional:            true,
				ElementType:         types.StringType,
				// FIXME: Add Validator which ensures kind is set to only allowed types e.g. slave
				Validators: []validator.List{},
			},
			"soa": schema.SingleNestedAttribute{
				MarkdownDescription: "",
				Required:            true,
				Attributes: map[string]schema.Attribute{
					"create_record": schema.BoolAttribute{
						Description: "If set to false will not create the SOA record",
						Default:     booldefault.StaticBool(true),
						Optional:    true,
						Computed:    true,
					},
					"rname": schema.StringAttribute{
						Required:    true,
						Description: "The RName of the SOA record. Represents the administrator's email address. It will be prefixed with the zone name unless ending with an explicit '.'",
					},
					"refresh": schema.Int64Attribute{
						Description: "The length of time (in seconds) secondary servers should wait before asking primary servers for the SOA record to see if it has been updated.",
						Optional:    true,
						Default:     int64default.StaticInt64(86400),
						Computed:    true,
					},
					"retry": schema.Int64Attribute{
						Description: "The length of time a server should wait for asking an unresponsive primary nameserver for an update again.",
						Optional:    true,
						Default:     int64default.StaticInt64(7200),
						Computed:    true,
					},
					"expire": schema.Int64Attribute{
						Description: "If a secondary server does not get a response from the primary server for this amount of time, it should stop responding to queries for the zone.",
						Optional:    true,
						Default:     int64default.StaticInt64(4000000),
						Computed:    true,
					},
					"ttl": schema.Int64Attribute{
						Description: "TTL of the zone data",
						Optional:    true,
						Default:     int64default.StaticInt64(11200),
						Computed:    true,
					},
				},
			},
		},
	}
}

func (r *ZoneResource) Configure(ctx context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
	// Prevent panic if the provider has not been configured.
	if req.ProviderData == nil {
		return
	}

	providerData, ok := req.ProviderData.(*PDNSProviderData)

	if !ok {
		// resp.Diagnostics.AddError(
		//	"Unexpected Resource Configure Type",
		//	fmt.Sprintf("Expected *http.Client, got: %T. Please report this issue to the provider developers.", req.ProviderData.toString()),
		// )

		return
	}

	r.providerData = providerData
}

func (r *ZoneResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var data ZoneResourceModel

	resp.Diagnostics.Append(req.Plan.Get(ctx, &data)...)

	if resp.Diagnostics.HasError() {
		return
	}

	zone, err := r.providerData.pdnsClient.GetZone(ctx, data.Name.ValueString())

	var unauthorizedError *pdns_client.PDNSUnauthorizedError
	var notFoundError *pdns_client.PDNSZoneNotFoundError
	if err != nil && errors.As(err, &unauthorizedError) {
		resp.Diagnostics.AddError("Authorization Error", "Not authorized to access pdns api")
		return
	} else if err != nil && !errors.As(err, &notFoundError) {
		resp.Diagnostics.AddError("Client Error", fmt.Sprintf("Unable to do http request to pdns API, got error: %s", notFoundError.Error()))
		return
	}

	if zone.Name != "" {
		resp.Diagnostics.AddError("Zone already exists", "It seems that the zone does already exist. Maybe try importing instead")
		return
	}

	newZone, err := createZoneFromData(ctx, data)
	if err != nil {
		resp.Diagnostics.AddError("Zone Error", err.Error())
		return
	}

	zone, err = r.providerData.pdnsClient.CreateZone(ctx, newZone)
	if err != nil && errors.As(err, &unauthorizedError) {
		resp.Diagnostics.AddError("Authorization Error", "Not authorized to access pdns api")
		return
	} else if err != nil {
		resp.Diagnostics.AddError("Client Error", fmt.Sprintf("Unable to do http request to pdns API, got error: %s", err))
		return
	}

	// Write logs using the tflog package
	// Documentation: https://terraform.io/plugin/log
	tflog.Trace(ctx, "created a resource")

	resp.Diagnostics.Append(resp.State.Set(ctx, &data)...)
}

func createZoneFromData(ctx context.Context, data ZoneResourceModel) (pdns_client.PDNSZone, error) {
	name := data.Name.ValueString()

	nameservers := make([]Nameserver, 0, len(data.Nameservers.Elements()))
	diags := data.Nameservers.ElementsAs(ctx, &nameservers, false)
	if diags.HasError() {
		fmt.Printf("Error> %v", diags)
		return pdns_client.PDNSZone{}, fmt.Errorf("Error handling data: ERROR")
	}

	var soa SOA
	diags = data.SOA.As(ctx, &soa, basetypes.ObjectAsOptions{})
	if diags.HasError() {
		fmt.Printf("Error> %v", diags)
		return pdns_client.PDNSZone{}, fmt.Errorf("Error handling data: ERROR")
	}
	soa.RName = lo.Ternary(strings.HasSuffix(soa.RName, "."), soa.RName, soa.RName+"."+name)

	nameservers = lo.Map(nameservers, func(item Nameserver, index int) Nameserver {
		if !strings.HasSuffix(item.Hostname, ".") {
			item.Hostname = item.Hostname + "." + name
		}
		return item
	})

	soaModel := SOAToSOAModel(soa)
	objectValue, diags := types.ObjectValueFrom(ctx, soaModel.AttributeTypes(), soaModel)
	if diags.HasError() {
		return pdns_client.PDNSZone{}, fmt.Errorf("Parser error", "Failed to parse soa back to data model")
	}
	data.SOA = objectValue

	records := make([]pdns_client.Rrset, 0)

	if soa.CreateRecord {
		serial := time.Now().Format("20060102") + "01"
		records = append(records, pdns_client.Rrset{
			Type: "SOA",
			Name: name,
			Records: []pdns_client.Record{
				{
					Content: fmt.Sprintf(
						"%s %s %s %d %d %d %d",
						nameservers[0].Hostname,
						soa.RName,
						serial,
						soa.Refresh,
						soa.Retry,
						soa.Expire,
						soa.TTL,
					),
				},
			},
		})
	}

	for _, nameserver := range nameservers {
		if !nameserver.CreateRecord {
			continue
		}

		var recordType string

		parsedIP := net.ParseIP(nameserver.Address)
		if parsedIP == nil {
			fmt.Println("Invalid IP address")
			return pdns_client.PDNSZone{}, fmt.Errorf("Failed to parse IP address")
		}

		if parsedIP.To4() != nil {
			recordType = "A"
		} else {
			recordType = "AAAA"
		}

		records = append(records, pdns_client.Rrset{
			Type: recordType,
			Name: nameserver.Hostname,
			Records: []pdns_client.Record{
				{
					Content: nameserver.Address,
				},
			},
		})
	}

	newZone := pdns_client.PDNSZone{
		Name:   name,
		Kind:   data.Kind.ValueString(),
		Dnssec: false,
		Nameservers: lo.Map(nameservers, func(item Nameserver, index int) string {
			return item.Hostname
		}),
		Rrsets: records,
	}

	return newZone, nil
}

func (r *ZoneResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var data ZoneResourceModel

	// Read Terraform prior state data into the model
	resp.Diagnostics.Append(req.State.Get(ctx, &data)...)

	if resp.Diagnostics.HasError() {
		return
	}

	zone, err := r.providerData.pdnsClient.GetZone(ctx, data.Name.ValueString())

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

	data.DNSSec = types.BoolValue(zone.Dnssec)
	data.Kind = types.StringValue(zone.Kind)
	data.Name = types.StringValue(zone.Name)

	soaRecord, isFound := lo.Find(zone.Rrsets, func(item pdns_client.Rrset) bool {
		return item.Type == "SOA" && item.Name == data.Name.ValueString()
	})

	if isFound {
		soaData := strings.Split(soaRecord.Records[0].Content, " ")

		refresh, err := strconv.ParseInt(soaData[3], 10, 64)
		if err != nil {
			resp.Diagnostics.AddError("Parsing Error", fmt.Sprintf("Failed to parse SOA number: %s", err.Error()))
			return
		}

		retry, err := strconv.ParseInt(soaData[4], 10, 64)
		if err != nil {
			resp.Diagnostics.AddError("Parsing Error", fmt.Sprintf("Failed to parse SOA number: %s", err.Error()))
			return
		}

		expire, err := strconv.ParseInt(soaData[5], 10, 64)
		if err != nil {
			resp.Diagnostics.AddError("Parsing Error", fmt.Sprintf("Failed to parse SOA number: %s", err.Error()))
			return
		}

		ttl, err := strconv.ParseInt(soaData[6], 10, 64)
		if err != nil {
			resp.Diagnostics.AddError("Parsing Error", fmt.Sprintf("Failed to parse SOA number: %s", err.Error()))
			return
		}

		var currentSoaData SOA
		diags := data.SOA.As(ctx, &currentSoaData, basetypes.ObjectAsOptions{})
		if diags.HasError() {
			resp.Diagnostics = append(resp.Diagnostics, diags...)
			return
		}

		soa := SOAModel{
			RName:        types.StringValue(strings.ReplaceAll(soaData[1], "."+zone.Name, "")),
			Refresh:      types.Int64Value(refresh),
			Retry:        types.Int64Value(retry),
			Expire:       types.Int64Value(expire),
			TTL:          types.Int64Value(ttl),
			CreateRecord: types.BoolValue(currentSoaData.CreateRecord),
		}

		objectValue, diags := types.ObjectValueFrom(ctx, soa.AttributeTypes(), soa)
		if diags.HasError() {
			resp.Diagnostics = append(resp.Diagnostics, diags...)
			return
		}

		data.SOA = objectValue
	}

	currentNameservers := make([]Nameserver, 0, len(data.Nameservers.Elements()))
	diags := data.Nameservers.ElementsAs(ctx, &currentNameservers, false)
	if diags.HasError() {
		resp.Diagnostics = append(resp.Diagnostics, diags...)
		return
	}

	nsDataTypes := map[string]attr.Type{
		"address":       types.StringType,
		"hostname":      types.StringType,
		"create_record": types.BoolType,
	}

	elements := lo.Map(currentNameservers, func(item Nameserver, index int) attr.Value {
		ns := lo.Ternary(strings.HasSuffix(item.Hostname, "."), item.Hostname, item.Hostname+"."+zone.Name)

		nsRecord, isFound := lo.Find(zone.Rrsets, func(itemRset pdns_client.Rrset) bool {
			if len(itemRset.Records) == 0 {
				return false
			}
			return (itemRset.Type == "A" || itemRset.Type == "AAAA") &&
				itemRset.Name == ns &&
				itemRset.Records[0].Content == item.Address
		})

		// FIXME: Error
		if !isFound {
			panic("No record found for " + item.Hostname)
		}

		nsData := map[string]attr.Value{
			"address":       types.StringValue(nsRecord.Records[0].Content),
			"hostname":      types.StringValue(strings.ReplaceAll(nsRecord.Name, "."+zone.Name, "")),
			"create_record": types.BoolValue(item.CreateRecord),
		}

		objValue, diags := types.ObjectValue(nsDataTypes, nsData)
		// FIXME: Error
		if diags.HasError() {
		}

		return objValue
	})

	listValue, diags := types.ListValue(types.ObjectType{AttrTypes: nsDataTypes}, elements)
	if diags.HasError() {
		resp.Diagnostics = append(resp.Diagnostics, diags...)
		return
	}

	data.Nameservers = listValue
	// Save updated data into Terraform state
	resp.Diagnostics.Append(resp.State.Set(ctx, &data)...)
}

// TODO: Update zone
func (r *ZoneResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan, state ZoneResourceModel

	// Read Terraform plan data into the model
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)

	if resp.Diagnostics.HasError() {
		return
	}

	resp.Diagnostics.AddError("Test", fmt.Sprintf("%v", req))
	resp.Diagnostics.AddError("Test", fmt.Sprintf("%v", state))
	resp.Diagnostics.AddError("Test", fmt.Sprintf("%v", plan))

	// TODO: use PATCH to update Rrsets (if new or deleted ns) soa changes
	// if previous first ns changed change SOA

	if !state.DNSSec.Equal(plan.DNSSec) || !state.Kind.Equal(plan.Kind) {
		// TODO: use put if kind or dnssec changed
	}

	return
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *ZoneResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var data ZoneResourceModel

	// Read Terraform prior state data into the model
	resp.Diagnostics.Append(req.State.Get(ctx, &data)...)

	if resp.Diagnostics.HasError() {
		return
	}

	err := r.providerData.pdnsClient.DeleteZone(ctx, data.Name.ValueString())

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

// TODO: Import zone by name
func (r *ZoneResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	resource.ImportStatePassthroughID(ctx, path.Root("name"), req, resp)
}
