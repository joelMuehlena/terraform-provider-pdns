package provider

import (
	"context"
	"errors"
	"fmt"
	"net"
	"regexp"
	"slices"
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

	_ resource.ResourceWithValidateConfig = &ZoneResource{}
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
							Description: "The IP Address of the nameserver. The provider manages it as the A (IPv4) or AAAA (IPv6) glue record at the hostname, holding exactly this address, so it can only be set for nameservers inside the zone. If that record is changed, extended or removed outside of Terraform, the next plan shows the difference and the apply restores it.",
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
					"off and set `lock_dnsupdate = true` only on the zones that must refuse unsigned updates. " +
					"The placeholder is not a real key: never create a TSIG key named `pdns-provider-dnsupdate-locked` outside the provider " +
					"(with `pdnsutil` or the API), as it would be allowed to perform dynamic updates of every zone with `lock_dnsupdate = true`. " +
					"Defaults to `false`.",
				Optional: true,
				Computed: true,
				Default:  booldefault.StaticBool(false),
			},
			"masters": schema.ListAttribute{
				MarkdownDescription: "The primaries the zone is transferred from, as IP addresses with an optional port " +
					"(`192.0.2.1`, `2001:db8::1`, `192.0.2.1:5300` or `[2001:db8::1]:5300`). Only valid for `kind = \"Slave\"` " +
					"or `\"Consumer\"`; such zones are created with their primaries directly, also with `lock_dnsupdate = true` " +
					"(the temporary `Slave` of a locked create is only used for the other kinds and has no primaries). " +
					"Changes are applied in place.",
				Optional:    true,
				ElementType: types.StringType,
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
						Default:     int64default.StaticInt64(defaultSOARefresh),
						Computed:    true,
					},
					"retry": schema.Int64Attribute{
						Description: "The length of time a server should wait for asking an unresponsive primary nameserver for an update again.",
						Optional:    true,
						Default:     int64default.StaticInt64(defaultSOARetry),
						Computed:    true,
					},
					"expire": schema.Int64Attribute{
						Description: "If a secondary server does not get a response from the primary server for this amount of time, it should stop responding to queries for the zone.",
						Optional:    true,
						Default:     int64default.StaticInt64(defaultSOAExpire),
						Computed:    true,
					},
					"ttl": schema.Int64Attribute{
						Description: "TTL of the zone data",
						Optional:    true,
						Default:     int64default.StaticInt64(defaultSOATTL),
						Computed:    true,
					},
				},
			},
		},
	}
}

func (r *ZoneResource) ValidateConfig(ctx context.Context, req resource.ValidateConfigRequest, resp *resource.ValidateConfigResponse) {
	var data ZoneResourceModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &data)...)
	if resp.Diagnostics.HasError() {
		return
	}

	resp.Diagnostics.Append(validateMasters(data)...)
	resp.Diagnostics.Append(validateNameserverAddresses(data)...)
}

// validateNameserverAddresses rejects addresses of nameservers outside the
// zone: their glue A/AAAA record would be out of zone, which PowerDNS refuses.
func validateNameserverAddresses(data ZoneResourceModel) diag.Diagnostics {
	var diags diag.Diagnostics
	if data.Name.IsNull() || data.Name.IsUnknown() {
		return diags
	}
	zone := data.Name.ValueString()

	for i, element := range data.Nameservers.Elements() {
		nameserver, ok := element.(types.Object)
		if !ok || nameserver.IsNull() || nameserver.IsUnknown() {
			continue
		}
		hostname, _ := nameserver.Attributes()["hostname"].(types.String)
		address, _ := nameserver.Attributes()["address"].(types.String)
		if hostname.IsNull() || hostname.IsUnknown() || address.IsNull() || address.IsUnknown() {
			continue
		}
		if host := fqdn(hostname.ValueString(), zone); !inZone(host, zone) {
			diags.AddAttributeError(path.Root("nameservers").AtListIndex(i).AtName("address"), "Nameserver address outside the zone",
				fmt.Sprintf("The nameserver %s is not inside the zone %s, so the zone cannot hold its A/AAAA (glue) record and PowerDNS "+
					"rejects it as out of zone. Remove address and manage the record in the zone that contains %s.", host, zone, host))
		}
	}
	return diags
}

// inZone reports whether the fully qualified name is the zone apex or below it.
func inZone(name, zone string) bool {
	name, zone = strings.ToLower(name), strings.ToLower(zone)
	return name == zone || strings.HasSuffix(name, "."+zone)
}

// validateMasters checks that masters is only set for secondary zones and
// only lists IP addresses with an optional port.
func validateMasters(data ZoneResourceModel) diag.Diagnostics {
	var diags diag.Diagnostics
	if data.Masters.IsNull() || data.Masters.IsUnknown() {
		return diags
	}

	kind := data.Kind.ValueString()
	if data.Kind.IsNull() {
		kind = "Native"
	}
	if !data.Kind.IsUnknown() && !isSecondaryKind(kind) {
		diags.AddAttributeError(path.Root("masters"), "masters requires a secondary zone",
			fmt.Sprintf("masters lists the primaries a zone is transferred from and can only be set for zones of kind Slave or Consumer, not %s.", kind))
	}

	for i, element := range data.Masters.Elements() {
		primary, ok := element.(types.String)
		if !ok || primary.IsNull() || primary.IsUnknown() {
			continue
		}
		if _, ok := canonicalPrimary(primary.ValueString()); !ok {
			diags.AddAttributeError(path.Root("masters").AtListIndex(i), "Invalid primary",
				fmt.Sprintf("%q is not an IP address with an optional port, such as 192.0.2.1, 2001:db8::1, 192.0.2.1:5300 or [2001:db8::1]:5300.", primary.ValueString()))
		}
	}
	return diags
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

	newZone, _, diags := createZoneFromData(ctx, data)
	if diags.HasError() {
		resp.Diagnostics = append(resp.Diagnostics, diags...)
		return
	}

	serial, err := r.createZone(ctx, newZone, data.LockDNSUpdate.ValueBool())
	if handleClientError(&resp.Diagnostics, err) {
		return
	}
	data.Serial = types.StringValue(serial)

	resp.Diagnostics.Append(resp.State.Set(ctx, &data)...)
}

// isSecondaryKind reports whether zones of this kind are transferred from a
// primary. PowerDNS does not apply dynamic updates to them locally.
func isSecondaryKind(kind string) bool {
	return kind == "Slave" || kind == "Consumer"
}

// createZone creates the zone and then applies what the create request cannot
// carry (the dnsupdate lock, and the kind and masters of a locked create). If
// a step after the create request fails, the zone is deleted again: it is not
// in the state, so it would make the next apply fail with "Zone already
// exists". This holds for every create path. It returns the serial of the
// new zone on the server.
//
// Zones of a secondary kind, and all zones without lock, are created with
// their kind directly. Other zones with lock are created by createLocked.
func (r *ZoneResource) createZone(ctx context.Context, zone pdns_client.PDNSZone, lock bool) (string, error) {
	viaSlave := lock && !isSecondaryKind(zone.Kind)
	request := zone
	if viaSlave {
		request.Kind, request.Masters = "Slave", nil
	}

	if _, err := r.providerData.pdnsClient.CreateZone(ctx, request); err != nil {
		return "", err
	}

	var err error
	switch {
	case viaSlave:
		err = r.createLocked(ctx, zone)
	case lock:
		err = r.providerData.setDNSUpdateLock(ctx, zone.Name, true)
	}
	var serial string
	if err == nil {
		serial, err = r.zoneSerial(ctx, zone.Name)
	}
	if err != nil {
		if delErr := r.providerData.pdnsClient.DeleteZone(ctx, zone.Name); delErr != nil {
			return "", fmt.Errorf("%w (removing the partially created zone also failed: %v)", err, delErr)
		}
		return "", err
	}
	return serial, nil
}

// zoneSerial returns the serial of a zone on the server, as Read stores it.
// PowerDNS sets its own serial when the SOA record is not given
// (create_record = false) and changes it on writes (SOA-EDIT-API), so the
// serial the provider wrote is not necessarily the one the zone has.
func (r *ZoneResource) zoneSerial(ctx context.Context, name string) (string, error) {
	zone, err := r.providerData.pdnsClient.GetZone(ctx, name, false, "")
	if err != nil {
		return "", err
	}
	return strconv.FormatInt(zone.Serial, 10), nil
}

// createLocked finishes the create of a zone that never accepts unlocked
// dynamic updates. PowerDNS has no way to set metadata in the create request,
// so createZone creates the zone as a Slave without primaries (updates are
// forwarded to its primaries, of which there are none, and fail); it is then
// locked, and only then switched to its real kind. Records, TTLs and the
// serial are the same as when creating the zone with its kind directly.
// DNSSEC is enabled in the create request already: PowerDNS signs a Slave zone
// just like any other, and enabling it only with the kind switch fails for
// zones without a SOA record (create_record = false) because rectifying them
// fails, while sending the unchanged dnssec = true with the switch is a no-op.
// The temporary Slave has no primaries, so it neither transfers the zone nor
// forwards updates; any configured masters are only set after the switch.
// (masters are only valid for Slave and Consumer zones, which are created
// directly, so this only matters if the kind was unknown when the
// configuration was validated.)
func (r *ZoneResource) createLocked(ctx context.Context, zone pdns_client.PDNSZone) error {
	client := r.providerData.pdnsClient

	if err := r.providerData.setDNSUpdateLock(ctx, zone.Name, true); err != nil {
		return err
	}
	if err := client.UpdateZoneSettings(ctx, zone.Name, zone.Kind, zone.Dnssec); err != nil {
		return err
	}
	if len(zone.Masters) > 0 {
		return client.UpdateZoneMasters(ctx, zone.Name, zone.Masters)
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
		Dnssec: data.DNSSec.ValueBool(),
		Nameservers: lo.Map(nameservers, func(item Nameserver, index int) string {
			return item.Hostname
		}),
		Rrsets: records,
	}
	if diags := data.Masters.ElementsAs(ctx, &newZone.Masters, false); diags.HasError() {
		return pdns_client.PDNSZone{}, "", append(zoneDiags, diags...)
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
	data.Masters = mastersValue(zone.Masters, data.Masters)

	soa, diags := readSOA(ctx, zone, data.SOA)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}
	data.SOA = soa

	nameservers, diags := readZoneNameservers(ctx, zone, data.Nameservers)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}
	data.Nameservers = nameservers

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

	var newSoaData SOA
	resp.Diagnostics.Append(plan.SOA.As(ctx, &newSoaData, basetypes.ObjectAsOptions{})...)
	if resp.Diagnostics.HasError() {
		return
	}
	// With create_record = false the SOA record is not managed by the provider.
	soaChanged := newSoaData.CreateRecord && !state.SOA.Equal(plan.SOA)

	if !state.Nameservers.Equal(plan.Nameservers) || soaChanged {
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

		nsRecords, diags := nameserverRecordChanges(ctx, newNameservers, currentNameservers, plan.Name.ValueString())
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

		if newSoaData.CreateRecord && (soaChanged || (len(plan.Nameservers.Elements()) >= 1 && len(state.Nameservers.Elements()) == 0) || !state.Nameservers.Elements()[0].Equal(plan.Nameservers.Elements()[0])) {
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
	}

	if handleClientError(&resp.Diagnostics, r.updateSettings(ctx, plan, state)) {
		return
	}

	// The serial is computed and therefore unknown in the plan.
	serial, err := r.zoneSerial(ctx, plan.Name.ValueString())
	if handleClientError(&resp.Diagnostics, err) {
		return
	}
	plan.Serial = types.StringValue(serial)

	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

// updateSettings applies changes of the zone settings: masters, kind, DNSSEC
// and the dnsupdate lock. A lock that is added is set before, and a lock that
// is removed after the other changes, so a zone that switches from a
// secondary kind to one applying updates never does so unlocked.
func (r *ZoneResource) updateSettings(ctx context.Context, plan, state ZoneResourceModel) error {
	client := r.providerData.pdnsClient
	name := plan.Name.ValueString()
	lockChanged := !state.LockDNSUpdate.Equal(plan.LockDNSUpdate)
	lock := plan.LockDNSUpdate.ValueBool()

	if lockChanged && lock {
		if err := r.providerData.setDNSUpdateLock(ctx, name, true); err != nil {
			return err
		}
	}

	// The primaries are set before the kind switch, so a zone that becomes a
	// Slave transfers from the right servers; a zone that stops being one is
	// briefly a Slave without primaries, which neither transfers nor applies
	// updates.
	if !state.Masters.Equal(plan.Masters) {
		var masters []string
		if diags := plan.Masters.ElementsAs(ctx, &masters, false); diags.HasError() {
			return fmt.Errorf("failed to read masters: %v", diags)
		}
		if err := client.UpdateZoneMasters(ctx, name, masters); err != nil {
			return err
		}
	}

	if !state.DNSSec.Equal(plan.DNSSec) || !state.Kind.Equal(plan.Kind) {
		if err := client.UpdateZoneSettings(ctx, name, plan.Kind.ValueString(), plan.DNSSec.ValueBool()); err != nil {
			return err
		}
	}

	if lockChanged && !lock {
		return r.providerData.setDNSUpdateLock(ctx, name, false)
	}
	return nil
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

// ImportState imports a zone by its name. Read then builds soa and
// nameservers from the zone's records (see readSOA and importNameservers).
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

// pdnsDefaultSOAMName is the primary nameserver of the SOA record PowerDNS
// creates for a zone created without one (soa.create_record = false).
const pdnsDefaultSOAMName = "a.misconfigured.dns.server.invalid."

// Defaults of the soa attributes, also used for imported zones whose SOA
// record is not managed by the provider.
const (
	defaultSOARefresh = 86400
	defaultSOARetry   = 7200
	defaultSOAExpire  = 4000000
	defaultSOATTL     = 11200
)

// readSOA returns the soa attribute for the zone's SOA record, given the
// current one. With create_record = false the SOA record is not managed by
// the provider; PowerDNS creates a default one that must not be read back
// into the configured soa values.
//
// On import current is null and the attribute is built from the record
// alone: create_record is true unless the record is PowerDNS's default one.
// For such a zone only rname is taken from the record and the other values
// are the attribute defaults, as with create_record = false they are not
// written to the server.
func readSOA(ctx context.Context, zone pdns_client.PDNSZone, current types.Object) (types.Object, diag.Diagnostics) {
	soaRecord, isFound := lo.Find(zone.Rrsets, func(item pdns_client.Rrset) bool {
		return item.Type == "SOA" && item.Name == zone.Name && len(item.Records) > 0
	})
	if !isFound {
		return current, nil
	}
	content := soaRecord.Records[0].Content

	var configured SOA
	if current.IsNull() || current.IsUnknown() {
		configured.CreateRecord = !strings.HasPrefix(content, pdnsDefaultSOAMName+" ")
	} else {
		if diags := current.As(ctx, &configured, basetypes.ObjectAsOptions{}); diags.HasError() {
			return current, diags
		}
		if !configured.CreateRecord {
			return current, nil
		}
	}

	soa, err := soaModelFromRecord(content, zone.Name, configured.RName, configured.CreateRecord)
	if err != nil {
		var diags diag.Diagnostics
		diags.AddError("Parsing Error", fmt.Sprintf("Failed to read SOA record: %s", err))
		return current, diags
	}
	if !configured.CreateRecord {
		soa.Refresh = types.Int64Value(defaultSOARefresh)
		soa.Retry = types.Int64Value(defaultSOARetry)
		soa.Expire = types.Int64Value(defaultSOAExpire)
		soa.TTL = types.Int64Value(defaultSOATTL)
	}
	return types.ObjectValueFrom(ctx, soa.AttributeTypes(), soa)
}

// soaModelFromRecord parses the content of a SOA record
// ("mname rname serial refresh retry expire ttl") into a SOAModel. The rname
// keeps the configured spelling while it names the same address (see
// readName).
func soaModelFromRecord(content, zoneName, configuredRName string, createRecord bool) (SOAModel, error) {
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
		RName:        types.StringValue(readName(fields[1], configuredRName, zoneName)),
		Refresh:      types.Int64Value(numbers[0]),
		Retry:        types.Int64Value(numbers[1]),
		Expire:       types.Int64Value(numbers[2]),
		TTL:          types.Int64Value(numbers[3]),
		CreateRecord: types.BoolValue(createRecord),
	}, nil
}

// relativeName returns name relative to zone when it lies inside the zone,
// the spelling the provider accepts for names in the zone, and the fully
// qualified name otherwise. Only a suffix is removed, so names that merely
// contain the zone name elsewhere are kept intact.
func relativeName(name, zone string) string {
	suffix := "." + zone
	if len(name) > len(suffix) && strings.EqualFold(name[len(name)-len(suffix):], suffix) {
		return name[:len(name)-len(suffix)]
	}
	return name
}

// readName returns the name to store for a server value that was configured
// as configured. While both name the same domain, the configured spelling is
// kept, so "hostmaster" and "hostmaster.example.com." do not cause a diff;
// otherwise the server value (relative to the zone if inside it) shows the
// drift.
func readName(server, configured, zone string) string {
	if configured != "" && sameDNSName(server, fqdn(configured, zone)) {
		return configured
	}
	return relativeName(server, zone)
}

var nameserverAttrTypes = map[string]attr.Type{
	"address":  types.StringType,
	"hostname": types.StringType,
}

// readNameservers maps the configured nameservers to their current state.
// Hostnames keep their configured spelling. For a nameserver with an address,
// the address is read from the A or AAAA rrset at its hostname (see
// readAddress), so a glue record changed or removed out of band shows as a
// diff instead of failing the refresh. Nameservers without an address keep
// it unset, even if such a record exists: it is not managed by the provider.
func readNameservers(zone pdns_client.PDNSZone, current []Nameserver) (types.List, diag.Diagnostics) {
	listType := types.ObjectType{AttrTypes: nameserverAttrTypes}
	elements := make([]attr.Value, 0, len(current))

	for _, item := range current {
		address := item.Address
		if address != nil {
			address = readAddress(zone, fqdn(item.Hostname, zone.Name), *address)
		}

		objValue, diags := types.ObjectValue(nameserverAttrTypes, map[string]attr.Value{
			"address":  types.StringPointerValue(address),
			"hostname": types.StringValue(item.Hostname),
		})
		if diags.HasError() {
			return types.ListNull(listType), diags
		}
		elements = append(elements, objValue)
	}

	return types.ListValue(listType, elements)
}

// readZoneNameservers returns the nameservers attribute for the zone, given
// the current one, which is null on import.
func readZoneNameservers(ctx context.Context, zone pdns_client.PDNSZone, current types.List) (types.List, diag.Diagnostics) {
	if current.IsNull() || current.IsUnknown() {
		return types.ListValueFrom(ctx, types.ObjectType{AttrTypes: nameserverAttrTypes}, importNameservers(zone))
	}

	nameservers := make([]Nameserver, 0, len(current.Elements()))
	if diags := current.ElementsAs(ctx, &nameservers, false); diags.HasError() {
		return current, diags
	}
	return readNameservers(zone, nameservers)
}

// importNameservers builds the nameservers of an imported zone from its NS
// rrset. The provider writes the first nameserver as primary (mname) of the
// SOA record, so that one comes first; the others follow in the order the
// server returns them (PowerDNS sorts them). Hostnames inside the zone are
// relative to it, like the provider accepts them; their address is the
// first A record at that name, or else the first AAAA record (glue).
// Nameservers outside the zone get no address.
func importNameservers(zone pdns_client.PDNSZone) []Nameserver {
	nsRrset, _ := lo.Find(zone.Rrsets, func(rr pdns_client.Rrset) bool {
		return rr.Type == "NS" && sameDNSName(rr.Name, zone.Name)
	})
	records := slices.Clone(nsRrset.Records)

	soaRrset, _ := lo.Find(zone.Rrsets, func(rr pdns_client.Rrset) bool {
		return rr.Type == "SOA" && sameDNSName(rr.Name, zone.Name)
	})
	if fields := strings.Fields(lo.FirstOrEmpty(soaRrset.Records).Content); len(fields) > 0 {
		if i := slices.IndexFunc(records, func(r pdns_client.Record) bool { return sameDNSName(r.Content, fields[0]) }); i > 0 {
			primary := records[i]
			records = slices.Insert(slices.Delete(records, i, i+1), 0, primary)
		}
	}

	return lo.Map(records, func(record pdns_client.Record, _ int) Nameserver {
		ns := Nameserver{Hostname: relativeName(record.Content, zone.Name)}
		if ns.Hostname == record.Content {
			return ns
		}
		if glue := append(addressRecords(zone, record.Content, "A"), addressRecords(zone, record.Content, "AAAA")...); len(glue) > 0 {
			ns.Address = &glue[0]
		}
		return ns
	})
}

// addressRecords returns the contents of the rrset of the given type (A or
// AAAA) at name.
func addressRecords(zone pdns_client.PDNSZone, name, recordType string) []string {
	rrset, _ := lo.Find(zone.Rrsets, func(rr pdns_client.Rrset) bool {
		return rr.Type == recordType && sameDNSName(rr.Name, name)
	})
	return lo.Map(rrset.Records, func(r pdns_client.Record, _ int) string { return r.Content })
}

// readAddress returns the current address of a nameserver whose address is
// configured as configured. The provider writes the rrset of the address's
// type (A or AAAA) with exactly that one record, so:
//   - if that rrset holds only the configured address (compared as IP, so
//     different spellings of an IPv6 address match), configured is returned;
//   - if it holds other or additional records, the first record that differs
//     is returned, so the apply replaces the rrset with the configured address;
//   - if there is none, the first record of the other type is returned, if any;
//   - with no A or AAAA record at all, nil is returned (the glue record is
//     missing and is re-created on apply).
func readAddress(zone pdns_client.PDNSZone, hostname, configured string) *string {
	recordType, ok := addressRecordType(configured)
	if !ok {
		return &configured
	}
	sameIP := func(address string) bool { return net.ParseIP(address).Equal(net.ParseIP(configured)) }

	if records := addressRecords(zone, hostname, recordType); len(records) > 0 {
		if differing, found := lo.Find(records, func(r string) bool { return !sameIP(r) }); found {
			return &differing
		}
		return &configured
	}

	otherType := lo.Ternary(recordType == "A", "AAAA", "A")
	if records := addressRecords(zone, hostname, otherType); len(records) > 0 {
		return &records[0]
	}
	return nil
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

// canonicalPrimary returns a primary ("ip" or "ip:port") in the form PowerDNS
// stores it: the IP in its canonical form, the port only if it is not 53.
func canonicalPrimary(primary string) (string, bool) {
	host, port, err := net.SplitHostPort(primary)
	if err != nil {
		host, port = primary, "53"
	}
	ip := net.ParseIP(host)
	if ip == nil {
		return "", false
	}
	if n, err := strconv.ParseUint(port, 10, 16); err != nil || n == 0 {
		return "", false
	}
	if port == "53" {
		return ip.String(), true
	}
	return net.JoinHostPort(ip.String(), port), true
}

// mastersValue returns the primaries of a zone on the server as the masters
// attribute. It keeps the prior value if it lists the same primaries in
// another spelling (PowerDNS drops port 53 and lowercases IPv6 addresses),
// and a null prior value if the zone has no primaries.
func mastersValue(masters []string, prior types.List) types.List {
	if len(masters) == 0 && (prior.IsNull() || len(prior.Elements()) == 0) {
		return prior
	}

	priorElements := prior.Elements()
	same := len(priorElements) == len(masters)
	elements := make([]attr.Value, 0, len(masters))
	for i, primary := range masters {
		elements = append(elements, types.StringValue(primary))
		if !same {
			continue
		}
		p, ok := priorElements[i].(types.String)
		canonical, valid := canonicalPrimary(p.ValueString())
		server, _ := canonicalPrimary(primary)
		same = ok && valid && canonical == server
	}
	if same {
		return prior
	}
	return types.ListValueMust(types.StringType, elements)
}

// nameserverRecordChanges builds the A/AAAA (glue) rrset changes from the
// current to the planned nameservers: glue of added or changed nameservers is
// written, and glue that is no longer needed is deleted. That is the glue of
// removed nameservers, and of nameservers whose address was removed or changed
// to the other address family (Diff reports both as changed).
func nameserverRecordChanges(ctx context.Context, planned, current []Nameserver, zoneName string) ([]pdns_client.Rrset, diag.Diagnostics) {
	var diags diag.Diagnostics
	addedOrChanged, deleted := Diff(planned, current)
	previous := lo.KeyBy(current, func(ns Nameserver) string { return ns.Hostname })
	records := make([]pdns_client.Rrset, 0, len(addedOrChanged)+len(deleted))

	for i, nameserver := range addedOrChanged {
		var recordType string
		if nameserver.Address == nil {
			tflog.Info(ctx, "Skipping nameserver A or AAAA record, because Address is not set.", map[string]any{"path": path.Root("nameservers[" + strconv.Itoa(i) + "]").String()})
		} else {
			var ok bool
			recordType, ok = addressRecordType(*nameserver.Address)
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

		// The glue of the previous address is orphaned unless it was just
		// replaced by a record of the same type.
		if prev, ok := previous[nameserver.Hostname]; ok && prev.Address != nil {
			if prevType, ok := addressRecordType(*prev.Address); ok && prevType != recordType {
				records = append(records, pdns_client.Rrset{
					Type:       prevType,
					Changetype: "DELETE",
					Name:       fqdn(nameserver.Hostname, zoneName),
				})
			}
		}
	}

	for i, nameserver := range deleted {
		if nameserver.Address == nil {
			// No address, so there is no glue record to delete.
			continue
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
