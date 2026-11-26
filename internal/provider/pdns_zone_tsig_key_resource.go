package provider

import (
	"context"
	"fmt"
	"regexp"
	"strings"

	"github.com/hashicorp/terraform-plugin-framework-validators/stringvalidator"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/booldefault"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/samber/lo"
)

// metadataTSIGAllowDNSUpdate is the zone metadata kind listing the TSIG key
// names allowed to perform RFC2136 dynamic updates.
const metadataTSIGAllowDNSUpdate = "TSIG-ALLOW-DNSUPDATE"

var (
	_ resource.Resource                   = &ZoneTSIGKeyResource{}
	_ resource.ResourceWithImportState    = &ZoneTSIGKeyResource{}
	_ resource.ResourceWithConfigure      = &ZoneTSIGKeyResource{}
	_ resource.ResourceWithValidateConfig = &ZoneTSIGKeyResource{}
)

func NewZoneTSIGKeyResource() resource.Resource {
	return &ZoneTSIGKeyResource{}
}

type ZoneTSIGKeyResource struct {
	providerData *PDNSProviderData
}

type ZoneTSIGKeyResourceModel struct {
	ID        types.String `tfsdk:"id"`
	Zone      types.String `tfsdk:"zone"`
	TSIGKeyID types.String `tfsdk:"tsig_key_id"`
	DNSUpdate types.Bool   `tfsdk:"dnsupdate"`
	AXFR      types.Bool   `tfsdk:"axfr"`
	Secondary types.Bool   `tfsdk:"secondary"`
}

// tsigPurposes holds which purposes a key is attached to a zone for.
type tsigPurposes struct {
	DNSUpdate bool
	AXFR      bool
	Secondary bool
}

func (p tsigPurposes) any() bool {
	return p.DNSUpdate || p.AXFR || p.Secondary
}

func (m ZoneTSIGKeyResourceModel) purposes() tsigPurposes {
	return tsigPurposes{
		DNSUpdate: m.DNSUpdate.ValueBool(),
		AXFR:      m.AXFR.ValueBool(),
		Secondary: m.Secondary.ValueBool(),
	}
}

// zoneTSIGState is the current TSIG configuration of a zone as far as the
// attachment resource is concerned.
type zoneTSIGState struct {
	masterIDs      []string
	slaveIDs       []string
	dnsupdateNames []string
}

func (s zoneTSIGState) purposesFor(keyID, keyName string) tsigPurposes {
	return tsigPurposes{
		DNSUpdate: containsDNSName(s.dnsupdateNames, keyName),
		AXFR:      containsDNSName(s.masterIDs, keyID),
		Secondary: containsDNSName(s.slaveIDs, keyID),
	}
}

func (r *ZoneTSIGKeyResource) Metadata(ctx context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_zone_tsig_key"
}

func (r *ZoneTSIGKeyResource) Schema(ctx context.Context, req resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "Attaches a TSIG key to a zone. A key can be attached to many zones and a zone can have many keys; " +
			"use exactly one `pdns_zone_tsig_key` per zone and key pair, and enable one or more purposes on it.\n\n" +
			"`dnsupdate` only takes effect when the PowerDNS server runs with `dnsupdate=yes`; the source addresses allowed to send " +
			"updates are controlled by the `allow-dnsupdate-from` setting (or the `ALLOW-DNSUPDATE-FROM` zone metadata).",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				MarkdownDescription: "Identifier of the attachment in the form `<zone>/<tsig_key_id>`.",
				Computed:            true,
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.UseStateForUnknown(),
				},
			},
			"zone": schema.StringAttribute{
				MarkdownDescription: "Name of the zone to attach the key to. Must end with a dot `.`.",
				Required:            true,
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.RequiresReplace(),
				},
				Validators: []validator.String{
					stringvalidator.RegexMatches(regexp.MustCompile(`\.$`), "Name must end with a dot"),
				},
			},
			"tsig_key_id": schema.StringAttribute{
				MarkdownDescription: "ID of the TSIG key, usually `pdns_tsig_key.<name>.id`.",
				Required:            true,
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.RequiresReplace(),
				},
				Validators: []validator.String{
					stringvalidator.LengthAtLeast(1),
				},
			},
			"dnsupdate": schema.BoolAttribute{
				MarkdownDescription: "Allow the key to perform RFC2136 dynamic updates on the zone (`TSIG-ALLOW-DNSUPDATE` metadata). Defaults to `false`.",
				Optional:            true,
				Computed:            true,
				Default:             booldefault.StaticBool(false),
			},
			"axfr": schema.BoolAttribute{
				MarkdownDescription: "Allow zone transfers (AXFR) of the zone signed with this key (`master_tsig_key_ids` / `TSIG-ALLOW-AXFR`). Defaults to `false`.",
				Optional:            true,
				Computed:            true,
				Default:             booldefault.StaticBool(false),
			},
			"secondary": schema.BoolAttribute{
				MarkdownDescription: "Use the key when this server transfers the zone from its primary, for `Slave`/secondary zones (`slave_tsig_key_ids` / `AXFR-MASTER-TSIG`). Defaults to `false`.",
				Optional:            true,
				Computed:            true,
				Default:             booldefault.StaticBool(false),
			},
		},
	}
}

func (r *ZoneTSIGKeyResource) ValidateConfig(ctx context.Context, req resource.ValidateConfigRequest, resp *resource.ValidateConfigResponse) {
	var data ZoneTSIGKeyResourceModel

	resp.Diagnostics.Append(req.Config.Get(ctx, &data)...)
	if resp.Diagnostics.HasError() {
		return
	}

	if data.DNSUpdate.IsUnknown() || data.AXFR.IsUnknown() || data.Secondary.IsUnknown() {
		return
	}

	if !data.purposes().any() {
		resp.Diagnostics.AddAttributeError(
			path.Root("dnsupdate"),
			"No purpose enabled",
			"At least one of `dnsupdate`, `axfr` or `secondary` must be set to true.",
		)
	}
}

func (r *ZoneTSIGKeyResource) Configure(ctx context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
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

func (r *ZoneTSIGKeyResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan ZoneTSIGKeyResourceModel

	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}

	zone, keyID := plan.Zone.ValueString(), plan.TSIGKeyID.ValueString()

	// Resolving the key also validates that it exists.
	key, err := r.providerData.pdnsClient.GetTSIGKey(ctx, keyID)
	if handleClientError(&resp.Diagnostics, err) {
		return
	}

	unlock := r.providerData.lockZone(zone)
	defer unlock()

	if handleClientError(&resp.Diagnostics, r.apply(ctx, zone, keyID, key.Name, plan.purposes())) {
		return
	}

	plan.ID = types.StringValue(zone + "/" + keyID)
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *ZoneTSIGKeyResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var data ZoneTSIGKeyResourceModel

	resp.Diagnostics.Append(req.State.Get(ctx, &data)...)
	if resp.Diagnostics.HasError() {
		return
	}

	zone, keyID := data.Zone.ValueString(), data.TSIGKeyID.ValueString()

	keyName, err := r.keyName(ctx, keyID)
	if handleClientError(&resp.Diagnostics, err) {
		return
	}

	state, err := r.load(ctx, zone)
	if isNotFound(err) {
		resp.Diagnostics.AddWarning(
			"Zone not found",
			fmt.Sprintf("The zone %q no longer exists on the PowerDNS server, so the attachment of TSIG key %q was removed from state.", zone, keyID),
		)
		resp.State.RemoveResource(ctx)
		return
	}
	if handleClientError(&resp.Diagnostics, err) {
		return
	}

	current := state.purposesFor(keyID, keyName)
	if !current.any() {
		resp.Diagnostics.AddWarning(
			"TSIG key attachment not found",
			fmt.Sprintf("The TSIG key %q is no longer attached to zone %q for any purpose and was removed from state. It will be re-attached on the next apply.", keyID, zone),
		)
		resp.State.RemoveResource(ctx)
		return
	}

	data.ID = types.StringValue(zone + "/" + keyID)
	data.DNSUpdate = types.BoolValue(current.DNSUpdate)
	data.AXFR = types.BoolValue(current.AXFR)
	data.Secondary = types.BoolValue(current.Secondary)

	resp.Diagnostics.Append(resp.State.Set(ctx, &data)...)
}

func (r *ZoneTSIGKeyResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan ZoneTSIGKeyResourceModel

	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}

	zone, keyID := plan.Zone.ValueString(), plan.TSIGKeyID.ValueString()

	key, err := r.providerData.pdnsClient.GetTSIGKey(ctx, keyID)
	if handleClientError(&resp.Diagnostics, err) {
		return
	}

	unlock := r.providerData.lockZone(zone)
	defer unlock()

	if handleClientError(&resp.Diagnostics, r.apply(ctx, zone, keyID, key.Name, plan.purposes())) {
		return
	}

	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *ZoneTSIGKeyResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var data ZoneTSIGKeyResourceModel

	resp.Diagnostics.Append(req.State.Get(ctx, &data)...)
	if resp.Diagnostics.HasError() {
		return
	}

	zone, keyID := data.Zone.ValueString(), data.TSIGKeyID.ValueString()

	keyName, err := r.keyName(ctx, keyID)
	if handleClientError(&resp.Diagnostics, err) {
		return
	}

	unlock := r.providerData.lockZone(zone)
	defer unlock()

	err = r.apply(ctx, zone, keyID, keyName, tsigPurposes{})
	if isNotFound(err) {
		return
	}
	handleClientError(&resp.Diagnostics, err)
}

func (r *ZoneTSIGKeyResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	zone, keyID, ok := strings.Cut(req.ID, "/")
	if !ok || zone == "" || keyID == "" {
		resp.Diagnostics.AddError(
			"Invalid import ID",
			fmt.Sprintf("Expected an import ID in the form `<zone>/<tsig_key_id>` (e.g. `example.com./tenant-a.`), got %q.", req.ID),
		)
		return
	}

	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("id"), req.ID)...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("zone"), zone)...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("tsig_key_id"), keyID)...)
}

// keyName returns the name of the TSIG key, needed for metadata which lists
// key names rather than ids. If the key no longer exists the name is derived
// from the id, so stale attachments can still be read and cleaned up.
func (r *ZoneTSIGKeyResource) keyName(ctx context.Context, keyID string) (string, error) {
	key, err := r.providerData.pdnsClient.GetTSIGKey(ctx, keyID)
	if isNotFound(err) {
		return strings.TrimSuffix(keyID, "."), nil
	}
	if err != nil {
		return "", err
	}
	return key.Name, nil
}

func (r *ZoneTSIGKeyResource) load(ctx context.Context, zone string) (zoneTSIGState, error) {
	z, err := r.providerData.pdnsClient.GetZone(ctx, zone, false, "")
	if err != nil {
		return zoneTSIGState{}, err
	}

	names, err := r.providerData.pdnsClient.GetZoneMetadata(ctx, zone, metadataTSIGAllowDNSUpdate)
	if err != nil {
		return zoneTSIGState{}, err
	}

	return zoneTSIGState{
		masterIDs:      z.MasterTsigKeyIDS,
		slaveIDs:       z.SlaveTsigKeyIDS,
		dnsupdateNames: names,
	}, nil
}

// apply brings the zone's TSIG lists in line with want for the given key,
// writing only the lists that actually change. Callers must hold the zone
// lock, as this is a read-modify-write of zone-wide lists.
func (r *ZoneTSIGKeyResource) apply(ctx context.Context, zone, keyID, keyName string, want tsigPurposes) error {
	state, err := r.load(ctx, zone)
	if err != nil {
		return err
	}
	current := state.purposesFor(keyID, keyName)

	var master, slave *[]string
	if current.AXFR != want.AXFR {
		master = lo.ToPtr(setDNSName(state.masterIDs, keyID, want.AXFR))
	}
	if current.Secondary != want.Secondary {
		slave = lo.ToPtr(setDNSName(state.slaveIDs, keyID, want.Secondary))
	}
	if master != nil || slave != nil {
		if err := r.providerData.pdnsClient.UpdateZoneTSIGKeyIDs(ctx, zone, master, slave); err != nil {
			return err
		}
	}

	if current.DNSUpdate != want.DNSUpdate {
		names := setDNSName(state.dnsupdateNames, keyName, want.DNSUpdate)
		if err := r.providerData.pdnsClient.SetZoneMetadata(ctx, zone, metadataTSIGAllowDNSUpdate, names); err != nil {
			return err
		}
	}

	return nil
}

func containsDNSName(list []string, name string) bool {
	return lo.ContainsBy(list, func(item string) bool { return sameDNSName(item, name) })
}

// setDNSName returns a copy of list with name present (add=true) or absent
// (add=false), comparing entries as DNS names.
func setDNSName(list []string, name string, add bool) []string {
	out := lo.Reject(list, func(item string, _ int) bool { return sameDNSName(item, name) })
	if add {
		out = append(out, name)
	}
	return out
}
