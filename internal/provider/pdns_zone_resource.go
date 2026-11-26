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
	"github.com/hashicorp/terraform-plugin-framework/diag"
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
	Serial      types.String `tfsdk:"serial"`
	Kind        types.String `tfsdk:"kind"`
	SOA         types.Object `tfsdk:"soa"`
	DNSSec      types.Bool   `tfsdk:"dnssec"`
	// LockDNSUpdate keeps the dnsupdate lock placeholder in the zone.
	LockDNSUpdate types.Bool `tfsdk:"lock_dnsupdate"`
}

type Nameserver struct {
	Address  *string `tfsdk:"address"`
	Hostname string  `tfsdk:"hostname"`
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

// TODO: Further DNSSec functionality?
// TODO: Support for other kinds Like slave or master

func (r *ZoneResource) Schema(ctx context.Context, req resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "Manages a PowerDNS DNS zone, including its SOA and nameserver (NS) records.\n\n" +
			"**Dynamic updates:** unless the server runs with `dnsupdate-require-tsig=yes` (PowerDNS 5.0+), a zone without dnsupdate keys " +
			"accepts unsigned updates from `allow-dnsupdate-from`. Set that option, or `lock_dnsupdate = true` on older servers.",
		Attributes: map[string]schema.Attribute{
			"name": schema.StringAttribute{
				MarkdownDescription: "The Name of the zone to be created. Must end with a dot",
				Required:            true,
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.RequiresReplace(),
				},
				Validators: []validator.String{
					stringvalidator.RegexMatches(regexp.MustCompile(`\.$`), "Name must end with a dot"),
				},
			},
			"serial": schema.StringAttribute{
				MarkdownDescription: "The serial of the zone",
				Optional:            false,
				Required:            false,
				Computed:            true,
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
							Description: "The hostname of the nameservers. Will be suffixed with the zone name if not ending with an explicit '.'",
						},
						"address": schema.StringAttribute{
							Optional:    true,
							Description: "The IP Address of the nameserver",
							Validators: []validator.String{
								stringvalidator.RegexMatches(IP_REGEX, "The passed string is not a valid IPv4 or valid IPv6 Address"),
							},
						},
					},
				},
			},
			"kind": schema.StringAttribute{
				MarkdownDescription: "The zone kind. One of `Native`, `Master`, `Slave`, `Producer` or `Consumer`. Defaults to `Native`.",
				Optional:            true,
				Default:             stringdefault.StaticString("Native"),
				Computed:            true,
				Validators: []validator.String{
					stringvalidator.OneOf("Native", "Master", "Slave", "Producer", "Consumer"),
				},
			},
			"lock_dnsupdate": schema.BoolAttribute{
				MarkdownDescription: "Refuse unsigned RFC2136 dynamic updates for this zone, for servers without `dnsupdate-require-tsig`. " +
					"By default PowerDNS only requires a signature while the zone's `TSIG-ALLOW-DNSUPDATE` metadata lists a key; while it is " +
					"empty (a new zone, or after the last dnsupdate key was removed), unsigned updates from addresses permitted by " +
					"`allow-dnsupdate-from` are accepted. With `true`, the provider keeps the placeholder `" + dnsupdateLockKey + "` in that list, " +
					"so only updates signed by keys attached with `pdns_zone_tsig_key` (`dnsupdate = true`) are accepted, and none while no key " +
					"is attached. New zones are then created as `Slave` (which does not apply updates), locked and only then switched to their " +
					"`kind`, so they never accept unlocked updates. Setting it to `false` removes the placeholder.\n\n" +
					"On PowerDNS 5.0 and later, prefer the server setting `dnsupdate-require-tsig=yes`: it refuses every update that is not " +
					"signed by a key listed for the zone, for all zones and from the moment they exist, so this lock is not needed. " +
					"Use the lock on 4.9 (which does not have the setting) or where the server configuration cannot be changed. " +
					"The setting applies to every zone of the server; for per-zone control, also on 5.0+, leave `dnsupdate-require-tsig` " +
					"off and set `lock_dnsupdate = true` only on the zones that must refuse unsigned updates. Defaults to `false`.",
				Optional: true,
				Computed: true,
				Default:  booldefault.StaticBool(false),
			},
			"masters": schema.ListAttribute{
				MarkdownDescription: "Masters of this zone should only be set if kind is Slave",
				Optional:            true,
				ElementType:         types.StringType,
				// FIXME: Add Validator which ensures kind is set to only allowed types e.g. slave
				Validators: []validator.List{},
			},
			"soa": schema.SingleNestedAttribute{
				MarkdownDescription: "The Start Of Authority (SOA) record parameters for the zone.",
				Required:            true,
				Attributes: map[string]schema.Attribute{
					"create_record": schema.BoolAttribute{
						Description: "If set to false the provider will not create the SOA record",
						Default:     booldefault.StaticBool(true),
						Optional:    true,
						Computed:    true,
					},
					"rname": schema.StringAttribute{
						Required:    true,
						Description: "The RName of the SOA record. Represents the administrator's email address. It will be suffixed with the zone name unless ending with an explicit '.'",
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
		resp.Diagnostics.AddError("Parse Error", "Failed to parse provider data")
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

	zone, err := r.providerData.pdnsClient.GetZone(ctx, data.Name.ValueString(), false, "")

	var unauthorizedError *pdns_client.PDNSUnauthorizedError
	var notFoundError *pdns_client.PDNSZoneNotFoundError
	if err != nil && errors.As(err, &unauthorizedError) {
		resp.Diagnostics.AddError("Authorization Error", "Not authorized to access pdns api")
		return
	} else if err != nil && !errors.As(err, &notFoundError) {
		resp.Diagnostics.AddError("Client Error", fmt.Sprintf("Unable to do http request to pdns API, got error: %s", err.Error()))
		return
	}

	if zone.Name != "" {
		resp.Diagnostics.AddError("Zone already exists", "It seems that the zone does already exist. Maybe try importing instead")
		return
	}

	newZone, serial, diags := createZoneFromData(ctx, data)
	if diags.HasError() {
		resp.Diagnostics = append(resp.Diagnostics, diags...)
		return
	}

	data.Serial = types.StringValue(serial)

	if !data.LockDNSUpdate.ValueBool() || isSecondaryKind(newZone.Kind) {
		_, err = r.providerData.pdnsClient.CreateZone(ctx, newZone)
		if handleClientError(&resp.Diagnostics, err) {
			return
		}
		if data.LockDNSUpdate.ValueBool() &&
			handleClientError(&resp.Diagnostics, r.providerData.setDNSUpdateLock(ctx, newZone.Name, true)) {
			return
		}
		resp.Diagnostics.Append(resp.State.Set(ctx, &data)...)
		return
	}

	if handleClientError(&resp.Diagnostics, r.createLocked(ctx, newZone)) {
		return
	}

	resp.Diagnostics.Append(resp.State.Set(ctx, &data)...)
}

// isSecondaryKind reports whether zones of this kind are transferred from a
// primary. PowerDNS does not apply dynamic updates to them locally.
func isSecondaryKind(kind string) bool {
	return kind == "Slave" || kind == "Consumer"
}

// createLocked creates a zone that never accepts unlocked dynamic updates.
// PowerDNS has no way to set metadata in the create request, so the zone is
// created as a Slave without primaries (updates are forwarded to its
// primaries, of which there are none, and fail), then locked, and only then
// switched to its real kind. Records, TTLs and the serial are the same as when
// creating the zone with its kind directly. If a step after the create fails,
// the zone is deleted again so it is not left half configured.
func (r *ZoneResource) createLocked(ctx context.Context, zone pdns_client.PDNSZone) error {
	client := r.providerData.pdnsClient
	kind := zone.Kind
	zone.Kind = "Slave"

	if _, err := client.CreateZone(ctx, zone); err != nil {
		return err
	}

	err := r.providerData.setDNSUpdateLock(ctx, zone.Name, true)
	if err == nil {
		err = client.UpdateZoneSettings(ctx, zone.Name, kind, zone.Dnssec)
	}
	if err != nil {
		if delErr := client.DeleteZone(ctx, zone.Name); delErr != nil {
			return fmt.Errorf("%w (removing the partially created zone also failed: %v)", err, delErr)
		}
		return err
	}
	return nil
}

func createZoneFromData(ctx context.Context, data ZoneResourceModel) (pdns_client.PDNSZone, string, diag.Diagnostics) {
	name := data.Name.ValueString()
	zoneDiags := make([]diag.Diagnostic, 0)

	nameservers := make([]Nameserver, 0, len(data.Nameservers.Elements()))
	diags := data.Nameservers.ElementsAs(ctx, &nameservers, false)
	if diags.HasError() {
		zoneDiags = append(zoneDiags, diags...)
		return pdns_client.PDNSZone{}, "", zoneDiags
	}

	var soa SOA
	diags = data.SOA.As(ctx, &soa, basetypes.ObjectAsOptions{})
	if diags.HasError() {
		zoneDiags = append(zoneDiags, diags...)
		return pdns_client.PDNSZone{}, "", zoneDiags
	}
	soa.RName = fqdn(soa.RName, name)

	nameservers = lo.Map(nameservers, func(item Nameserver, index int) Nameserver {
		item.Hostname = fqdn(item.Hostname, name)
		return item
	})

	soaModel := SOAToSOAModel(soa)
	objectValue, diags := types.ObjectValueFrom(ctx, soaModel.AttributeTypes(), soaModel)
	if diags.HasError() {
		zoneDiags = append(zoneDiags, diag.NewErrorDiagnostic("Parser error", "Failed to parse soa back to data model"))
		return pdns_client.PDNSZone{}, "", zoneDiags
	}
	data.SOA = objectValue

	records := make([]pdns_client.Rrset, 0)

	serial := time.Now().Format("20060102") + "01"
	if soa.CreateRecord {
		records = append(records, soaRrset(soa, nameservers[0].Hostname, name, serial))
	}

	for index, nameserver := range nameservers {
		if nameserver.Address == nil {
			continue
		}

		var recordType string

		parsedIP := net.ParseIP(*nameserver.Address)
		if parsedIP == nil {
			zoneDiags = append(zoneDiags, diag.NewAttributeErrorDiagnostic(path.Root("nameservers").AtListIndex(index).AtMapKey("address"), "Parse Error", "Invalid IPv4 or IPv6 address"))
			return pdns_client.PDNSZone{}, "", zoneDiags
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
					Content: *nameserver.Address,
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

	return newZone, lo.Ternary(soa.CreateRecord, serial, ""), nil
}

func (r *ZoneResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var data ZoneResourceModel

	// Read Terraform prior state data into the model
	resp.Diagnostics.Append(req.State.Get(ctx, &data)...)

	if resp.Diagnostics.HasError() {
		return
	}

	zone, err := r.providerData.pdnsClient.GetZone(ctx, data.Name.ValueString(), true, "")
	if isNotFound(err) {
		resp.Diagnostics.AddWarning(
			"Zone not found",
			fmt.Sprintf("The zone %q no longer exists on the PowerDNS server and was removed from state. It will be re-created on the next apply.", data.Name.ValueString()),
		)
		resp.State.RemoveResource(ctx)
		return
	}
	if handleClientError(&resp.Diagnostics, err) {
		return
	}

	data.DNSSec = types.BoolValue(zone.Dnssec)
	data.Kind = types.StringValue(zone.Kind)
	data.Name = types.StringValue(zone.Name)
	data.Serial = types.StringValue(fmt.Sprintf("%d", zone.Serial))

	soaRecord, isFound := lo.Find(zone.Rrsets, func(item pdns_client.Rrset) bool {
		return item.Type == "SOA" && item.Name == data.Name.ValueString()
	})

	var currentSoaData SOA
	if isFound {
		diags := data.SOA.As(ctx, &currentSoaData, basetypes.ObjectAsOptions{})
		if diags.HasError() {
			resp.Diagnostics = append(resp.Diagnostics, diags...)
			return
		}
	}

	// With create_record = false the SOA record is not managed by the
	// provider; PowerDNS creates a default one that must not be read back
	// into the configured soa values.
	if isFound && currentSoaData.CreateRecord {
		soa, err := soaModelFromRecord(soaRecord.Records[0].Content, zone.Name, currentSoaData.CreateRecord)
		if err != nil {
			resp.Diagnostics.AddError("Parsing Error", fmt.Sprintf("Failed to read SOA record: %s", err))
			return
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

	listValue, diags := readNameservers(zone, currentNameservers)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}

	data.Nameservers = listValue

	locked, err := r.providerData.hasDNSUpdateLock(ctx, zone.Name)
	if handleClientError(&resp.Diagnostics, err) {
		return
	}
	data.LockDNSUpdate = types.BoolValue(locked)
	// Save updated data into Terraform state
	resp.Diagnostics.Append(resp.State.Set(ctx, &data)...)
}

func (r *ZoneResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan, state ZoneResourceModel

	// Read Terraform plan data into the model
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)

	if resp.Diagnostics.HasError() {
		return
	}

	// The serial is computed and therefore unknown in the plan. It only changes
	// when records are rewritten below; otherwise keep the current one.
	plan.Serial = state.Serial

	if !state.Nameservers.Equal(plan.Nameservers) || !state.SOA.Equal(plan.SOA) {
		records := make([]pdns_client.Rrset, 0)

		serial, err := IncreaseSOASerial(state.Serial.ValueString())
		if err != nil {
			resp.Diagnostics.AddError("Client error", fmt.Sprintf("Failed to increase SOA serial: %s", err.Error()))
			return
		}

		currentNameservers := make([]Nameserver, 0, len(state.Nameservers.Elements()))
		diags := state.Nameservers.ElementsAs(ctx, &currentNameservers, false)
		if diags.HasError() {
			resp.Diagnostics = append(resp.Diagnostics, diags...)
			return
		}
		newNameservers := make([]Nameserver, 0, len(plan.Nameservers.Elements()))
		diags = plan.Nameservers.ElementsAs(ctx, &newNameservers, false)
		if diags.HasError() {
			resp.Diagnostics = append(resp.Diagnostics, diags...)
			return
		}

		addedOrChanged, deleted := Diff(newNameservers, currentNameservers)

		tflog.Info(ctx, "DBG", map[string]any{"added": addedOrChanged, "del": deleted})

		nsRecords, diags := nameserverRecordChanges(ctx, addedOrChanged, deleted, plan.Name.ValueString())
		resp.Diagnostics.Append(diags...)
		if resp.Diagnostics.HasError() {
			return
		}
		records = append(records, nsRecords...)

		records = append(records, pdns_client.Rrset{
			Type:       "NS",
			Changetype: "REPLACE",
			Name:       plan.Name.ValueString(),
			Records: lo.Map(newNameservers, func(item Nameserver, index int) pdns_client.Record {
				return pdns_client.Record{
					Content: fqdn(item.Hostname, plan.Name.ValueString()),
				}
			}),
		})

		if !state.SOA.Equal(plan.SOA) || (len(plan.Nameservers.Elements()) >= 1 && len(state.Nameservers.Elements()) == 0) || !state.Nameservers.Elements()[0].Equal(plan.Nameservers.Elements()[0]) {
			var newSoaData SOA
			diags = plan.SOA.As(ctx, &newSoaData, basetypes.ObjectAsOptions{})
			if diags.HasError() {
				resp.Diagnostics = append(resp.Diagnostics, diags...)
				return
			}

			soaRecord := soaRrset(newSoaData, fqdn(newNameservers[0].Hostname, plan.Name.ValueString()), plan.Name.ValueString(), serial)
			soaRecord.Changetype = "REPLACE"
			records = append(records, soaRecord)
		}

		tflog.Debug(ctx, "Updating records", map[string]any{
			"records": records,
		})

		err = r.providerData.pdnsClient.UpdateZoneRecords(ctx, plan.Name.ValueString(), records)
		if handleClientError(&resp.Diagnostics, err) {
			return
		}

		plan.Serial = types.StringValue(serial)
	}

	if !state.DNSSec.Equal(plan.DNSSec) || !state.Kind.Equal(plan.Kind) {
		err := r.providerData.pdnsClient.UpdateZoneSettings(ctx, plan.Name.ValueString(), plan.Kind.ValueString(), plan.DNSSec.ValueBool())
		if handleClientError(&resp.Diagnostics, err) {
			return
		}
	}

	if !state.LockDNSUpdate.Equal(plan.LockDNSUpdate) {
		if handleClientError(&resp.Diagnostics, r.providerData.setDNSUpdateLock(ctx, plan.Name.ValueString(), plan.LockDNSUpdate.ValueBool())) {
			return
		}
	}

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
	if isNotFound(err) {
		return
	}
	handleClientError(&resp.Diagnostics, err)
}

// TODO: Import zone by name
func (r *ZoneResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	resource.ImportStatePassthroughID(ctx, path.Root("name"), req, resp)
}

// soaRrset builds the SOA rrset of a zone. primaryNS and soa.RName must
// already be fully qualified.
func soaRrset(soa SOA, primaryNS, zoneName, serial string) pdns_client.Rrset {
	return pdns_client.Rrset{
		Type: "SOA",
		Name: zoneName,
		Records: []pdns_client.Record{
			{
				Content: fmt.Sprintf(
					"%s %s %s %d %d %d %d",
					primaryNS,
					fqdn(soa.RName, zoneName),
					serial,
					soa.Refresh,
					soa.Retry,
					soa.Expire,
					soa.TTL,
				),
			},
		},
	}
}

// soaModelFromRecord parses the content of a SOA record
// ("mname rname serial refresh retry expire ttl") into a SOAModel.
func soaModelFromRecord(content, zoneName string, createRecord bool) (SOAModel, error) {
	fields := strings.Split(content, " ")
	if len(fields) < 7 {
		return SOAModel{}, fmt.Errorf("SOA record %q has %d fields, expected 7", content, len(fields))
	}

	numbers := make([]int64, 0, 4)
	for _, field := range fields[3:7] {
		n, err := strconv.ParseInt(field, 10, 64)
		if err != nil {
			return SOAModel{}, fmt.Errorf("failed to parse SOA number: %w", err)
		}
		numbers = append(numbers, n)
	}

	return SOAModel{
		RName:        types.StringValue(strings.ReplaceAll(fields[1], "."+zoneName, "")),
		Refresh:      types.Int64Value(numbers[0]),
		Retry:        types.Int64Value(numbers[1]),
		Expire:       types.Int64Value(numbers[2]),
		TTL:          types.Int64Value(numbers[3]),
		CreateRecord: types.BoolValue(createRecord),
	}, nil
}

var nameserverAttrTypes = map[string]attr.Type{
	"address":  types.StringType,
	"hostname": types.StringType,
}

// readNameservers maps the configured nameservers to their current state,
// using the A/AAAA records of the zone for nameservers with an address.
func readNameservers(zone pdns_client.PDNSZone, current []Nameserver) (types.List, diag.Diagnostics) {
	var diags diag.Diagnostics

	elements := lo.FilterMap(current, func(item Nameserver, index int) (attr.Value, bool) {
		ns := fqdn(item.Hostname, zone.Name)

		nsRecord, isFound := lo.Find(zone.Rrsets, func(itemRset pdns_client.Rrset) bool {
			if len(itemRset.Records) == 0 || item.Address == nil {
				return false
			}
			return (itemRset.Type == "A" || itemRset.Type == "AAAA") &&
				itemRset.Name == ns &&
				itemRset.Records[0].Content == *item.Address
		})

		var nsData map[string]attr.Value

		switch {
		case !isFound && item.Address != nil:
			diags.AddError("Read error", fmt.Sprintf("Failed to read A or AAAA record for NS record (%s)", item.Hostname))
			return nil, false
		case !isFound:
			nsData = map[string]attr.Value{
				"address":  types.StringPointerValue(item.Address),
				"hostname": types.StringValue(item.Hostname),
			}
		default:
			nsData = map[string]attr.Value{
				"address":  types.StringValue(nsRecord.Records[0].Content),
				"hostname": types.StringValue(strings.ReplaceAll(nsRecord.Name, "."+zone.Name, "")),
			}
		}

		objValue, objDiags := types.ObjectValue(nameserverAttrTypes, nsData)
		diags.Append(objDiags...)
		if objDiags.HasError() {
			return nil, false
		}

		return objValue, true
	})

	if diags.HasError() {
		return types.ListNull(types.ObjectType{AttrTypes: nameserverAttrTypes}), diags
	}

	listValue, listDiags := types.ListValue(types.ObjectType{AttrTypes: nameserverAttrTypes}, elements)
	diags.Append(listDiags...)
	return listValue, diags
}

// addressRecordType returns "A" or "AAAA" for an IP address, or false if the
// address is not a valid IP.
func addressRecordType(address string) (string, bool) {
	ip := net.ParseIP(address)
	if ip == nil {
		return "", false
	}
	if ip.To4() != nil {
		return "A", true
	}
	return "AAAA", true
}

// nameserverRecordChanges builds the A/AAAA rrset changes for added or changed
// and for removed nameservers.
func nameserverRecordChanges(ctx context.Context, addedOrChanged, deleted []Nameserver, zoneName string) ([]pdns_client.Rrset, diag.Diagnostics) {
	var diags diag.Diagnostics
	records := make([]pdns_client.Rrset, 0, len(addedOrChanged)+len(deleted))

	for i, nameserver := range addedOrChanged {
		if nameserver.Address == nil {
			tflog.Info(ctx, "Skipping nameserver A or AAAA record, because Address is not set.", map[string]any{"path": path.Root("nameservers[" + strconv.Itoa(i) + "]").String()})
			continue
		}

		recordType, ok := addressRecordType(*nameserver.Address)
		if !ok {
			diags.AddAttributeError(path.Root("nameservers["+strconv.Itoa(i)+"]"), "Parse Error", "Found invalid IPv4 or IPv6 address")
			return nil, diags
		}

		records = append(records, pdns_client.Rrset{
			Type:       recordType,
			Changetype: "REPLACE",
			Name:       fqdn(nameserver.Hostname, zoneName),
			Records: []pdns_client.Record{
				{
					Content: *nameserver.Address,
				},
			},
		})
	}

	for i, nameserver := range deleted {
		if nameserver.Address == nil {
			diags.AddAttributeError(path.Root("nameservers["+strconv.Itoa(i)+"]"), "Parse Error", "Address in removed nameserver is null")
			return nil, diags
		}

		recordType, ok := addressRecordType(*nameserver.Address)
		if !ok {
			diags.AddAttributeError(path.Root("nameservers["+strconv.Itoa(i)+"]"), "Parse Error", "Found invalid IPv4 or IPv6 address")
			return nil, diags
		}

		records = append(records, pdns_client.Rrset{
			Type:       recordType,
			Changetype: "DELETE",
			Name:       fqdn(nameserver.Hostname, zoneName),
		})
	}

	return records, diags
}
