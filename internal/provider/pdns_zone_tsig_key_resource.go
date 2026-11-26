package provider

import (
	"context"
	"fmt"
	"math/rand/v2"
	"regexp"
	"strings"
	"time"

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
	// retryConfig overrides defaultApplyRetry; nil in normal use.
	retryConfig *applyRetry
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
			"updates are controlled by the `allow-dnsupdate-from` setting (or the `ALLOW-DNSUPDATE-FROM` zone metadata).\n\n" +
			"**Concurrency:** PowerDNS stores the TSIG keys of a zone as one list per purpose and has no atomic add or remove, so every " +
			"change rewrites the whole list. Changes within one run are serialized. Across parallel runs, each change is re-read, " +
			"retried on conflict and re-checked after one second. This is not a guarantee: to rule out lost or reappearing entries, " +
			"serialize applies that touch the same zone.",
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
				MarkdownDescription: "Allow the key to perform RFC2136 dynamic updates on the zone (`TSIG-ALLOW-DNSUPDATE` metadata). Defaults to `false`. " +
					"PowerDNS only requires a signature while at least one key is allowed: once no key of the zone has `dnsupdate`, " +
					"updates from addresses permitted by `allow-dnsupdate-from` are accepted unsigned, unless the server runs with " +
					"`dnsupdate-require-tsig=yes` (PowerDNS 5.0+) or the zone is locked (`lock_dnsupdate` of `pdns_zone`).",
				Optional: true,
				Computed: true,
				Default:  booldefault.StaticBool(false),
			},
			"axfr": schema.BoolAttribute{
				MarkdownDescription: "Allow zone transfers (AXFR) of the zone signed with this key (`master_tsig_key_ids` / `TSIG-ALLOW-AXFR`). Defaults to `false`.",
				Optional:            true,
				Computed:            true,
				Default:             booldefault.StaticBool(false),
			},
			"secondary": schema.BoolAttribute{
				MarkdownDescription: "Use the key when this server transfers the zone from its primary, for `Slave`/secondary zones (`slave_tsig_key_ids` / `AXFR-MASTER-TSIG`). Defaults to `false`. " +
					"PowerDNS allows only one secondary key per zone.",
				Optional: true,
				Computed: true,
				Default:  booldefault.StaticBool(false),
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

// applyRetry tunes the conflict retry of apply. PowerDNS has no atomic
// add/remove for the zone TSIG lists, so concurrent writers (e.g. separate
// runs) can overwrite each other.
type applyRetry struct {
	maxWrites int
	// settle is the delay before the final re-check after the zone matched.
	settle time.Duration
	// backoff returns the delay before the given retry (1-based).
	backoff func(attempt int) time.Duration
}

// jitteredBackoff is a jittered linear backoff of 50-250ms per attempt.
func jitteredBackoff(attempt int) time.Duration {
	return time.Duration(attempt) * time.Duration(50+rand.IntN(200)) * time.Millisecond
}

// defaultApplyRetry is used by resources without their own retry settings.
// It is a variable so tests can make it faster.
var defaultApplyRetry = applyRetry{
	maxWrites: 8,
	settle:    time.Second,
	backoff:   jitteredBackoff,
}

func (r *ZoneTSIGKeyResource) retry() applyRetry {
	if r.retryConfig != nil {
		return *r.retryConfig
	}
	return defaultApplyRetry
}

// apply brings the zone's TSIG lists in line with want for the given key.
// Lists are read-modify-written, so after each write the zone is re-read and
// the write retried if another writer interfered. Once the zone matches, it
// is re-checked after a short settle delay to catch writes still in flight.
// Callers must hold the zone lock to serialize writers within this process.
func (r *ZoneTSIGKeyResource) apply(ctx context.Context, zone, keyID, keyName string, want tsigPurposes) error {
	retry := r.retry()
	writes := 0
	settled := false

	for {
		state, err := r.load(ctx, zone)
		if err != nil {
			return err
		}
		current := state.purposesFor(keyID, keyName)

		if current == want {
			if writes == 0 || settled {
				return nil
			}
			settled = true
			if err := sleepCtx(ctx, retry.settle); err != nil {
				return err
			}
			continue
		}

		if writes >= retry.maxWrites {
			return fmt.Errorf("TSIG key lists of zone %s were modified concurrently; gave up after %d attempts", zone, writes)
		}
		if writes > 0 {
			if err := sleepCtx(ctx, retry.backoff(writes)); err != nil {
				return err
			}
		}
		writes++
		settled = false

		if err := r.write(ctx, zone, keyID, keyName, current, want); err != nil {
			return err
		}
	}
}

// write performs one read-modify-write per list that differs from want. Each
// list is re-read right before it is written to keep the race window small.
func (r *ZoneTSIGKeyResource) write(ctx context.Context, zone, keyID, keyName string, current, want tsigPurposes) error {
	client := r.providerData.pdnsClient

	if current.AXFR != want.AXFR || current.Secondary != want.Secondary {
		z, err := client.GetZone(ctx, zone, false, "")
		if err != nil {
			return err
		}
		var master, slave *[]string
		if current.AXFR != want.AXFR {
			master = new(setDNSName(z.MasterTsigKeyIDS, keyID, want.AXFR))
		}
		if current.Secondary != want.Secondary {
			slave = new(setDNSName(z.SlaveTsigKeyIDS, keyID, want.Secondary))
		}
		if err := client.UpdateZoneTSIGKeyIDs(ctx, zone, master, slave); err != nil {
			return err
		}
	}

	if current.DNSUpdate != want.DNSUpdate {
		names, err := client.GetZoneMetadata(ctx, zone, metadataTSIGAllowDNSUpdate)
		if err != nil {
			return err
		}
		if err := client.SetZoneMetadata(ctx, zone, metadataTSIGAllowDNSUpdate, setDNSName(names, keyName, want.DNSUpdate)); err != nil {
			return err
		}
	}

	return nil
}

func sleepCtx(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
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
