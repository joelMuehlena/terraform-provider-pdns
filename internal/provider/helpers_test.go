package provider

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/samber/lo"
	"gitlab.com/joelMuehlena/homelab/code/terraform/provider/terraform-provider-pdns/internal/pdns_client"
)

func TestFqdn(t *testing.T) {
	t.Parallel()
	tests := []struct{ name, zone, want string }{
		{"www", "example.com.", "www.example.com."},
		{"a.b", "example.com.", "a.b.example.com."},
		{"ns.other.org.", "example.com.", "ns.other.org."},
	}
	for _, tt := range tests {
		if got := fqdn(tt.name, tt.zone); got != tt.want {
			t.Errorf("fqdn(%q, %q) = %q, want %q", tt.name, tt.zone, got, tt.want)
		}
	}
}

func TestSameDNSName(t *testing.T) {
	t.Parallel()
	tests := []struct {
		a, b string
		want bool
	}{
		{"tenant-a", "tenant-a.", true},
		{"Tenant-A.", "tenant-a", true},
		{"tenant-a", "tenant-b", false},
		{"", ".", true},
	}
	for _, tt := range tests {
		if got := sameDNSName(tt.a, tt.b); got != tt.want {
			t.Errorf("sameDNSName(%q, %q) = %v, want %v", tt.a, tt.b, got, tt.want)
		}
	}
}

func TestIsNotFound(t *testing.T) {
	t.Parallel()
	tests := []struct {
		err  error
		want bool
	}{
		{&pdns_client.PDNSZoneNotFoundError{}, true},
		{&pdns_client.PDNSTSIGKeyNotFoundError{}, true},
		{fmt.Errorf("wrapped: %w", &pdns_client.PDNSZoneNotFoundError{}), true},
		{&pdns_client.PDNSUnauthorizedError{}, false},
		{errors.New("other"), false},
		{nil, false},
	}
	for _, tt := range tests {
		if got := isNotFound(tt.err); got != tt.want {
			t.Errorf("isNotFound(%v) = %v, want %v", tt.err, got, tt.want)
		}
	}
}

func TestHandleClientError(t *testing.T) {
	t.Parallel()
	tests := []struct {
		err         error
		wantSummary string
	}{
		{&pdns_client.PDNSUnauthorizedError{}, "Authorization Error"},
		{&pdns_client.PDNSZoneNotFoundError{ZoneID: "z."}, "Zone not found"},
		{&pdns_client.PDNSTSIGKeyNotFoundError{KeyID: "k."}, "TSIG key not found"},
		{errors.New("boom"), "Client Error"},
	}
	for _, tt := range tests {
		var diags diag.Diagnostics
		if !handleClientError(&diags, tt.err) {
			t.Errorf("handleClientError(%v) = false, want true", tt.err)
		}
		if len(diags) != 1 || diags[0].Summary() != tt.wantSummary {
			t.Errorf("handleClientError(%v) diags = %v, want summary %q", tt.err, diags, tt.wantSummary)
		}
	}

	var diags diag.Diagnostics
	if handleClientError(&diags, nil) || diags.HasError() {
		t.Error("handleClientError(nil) must return false and add no diagnostics")
	}
}

func TestIncreaseSOASerial(t *testing.T) {
	t.Parallel()
	today := time.Now().Format("20060102")

	tests := []struct {
		current, want string
		wantErr       bool
	}{
		{"2000010101", today + "01", false},
		{"", today + "01", false},
		{today + "01", today + "02", false},
		{today + "09", today + "10", false},
		{today + "xx", "", true},
	}
	for _, tt := range tests {
		got, err := IncreaseSOASerial(tt.current)
		if (err != nil) != tt.wantErr {
			t.Errorf("IncreaseSOASerial(%q) error = %v, wantErr %v", tt.current, err, tt.wantErr)
			continue
		}
		if got != tt.want {
			t.Errorf("IncreaseSOASerial(%q) = %q, want %q", tt.current, got, tt.want)
		}
	}
}

func hostnames(ns []Nameserver) []string {
	names := lo.Map(ns, func(n Nameserver, _ int) string { return n.Hostname })
	slices.Sort(names)
	return names
}

func TestDiff(t *testing.T) {
	t.Parallel()
	// Addresses are separate pointers with equal values, as produced by
	// decoding plan and state independently.
	current := []Nameserver{
		{Hostname: "ns1", Address: new("10.0.0.1")},
		{Hostname: "ns2", Address: new("10.0.0.2")},
		{Hostname: "ns3"},
	}
	planned := []Nameserver{
		{Hostname: "ns1", Address: new("10.0.0.1")}, // unchanged
		{Hostname: "ns2", Address: new("10.0.0.9")}, // changed
		{Hostname: "ns3"}, // unchanged, no address
		{Hostname: "ns4", Address: new("10.0.0.4")}, // added
	}

	addedOrChanged, deleted := Diff(planned, current)
	if got := hostnames(addedOrChanged); !slices.Equal(got, []string{"ns2", "ns4"}) {
		t.Errorf("addedOrChanged = %v, want [ns2 ns4]", got)
	}
	if len(deleted) != 0 {
		t.Errorf("deleted = %v, want none", hostnames(deleted))
	}

	addedOrChanged, deleted = Diff(current[:1], current)
	if len(addedOrChanged) != 0 {
		t.Errorf("addedOrChanged = %v, want none", hostnames(addedOrChanged))
	}
	if got := hostnames(deleted); !slices.Equal(got, []string{"ns2", "ns3"}) {
		t.Errorf("deleted = %v, want [ns2 ns3]", got)
	}

	// Removing an address is a change.
	addedOrChanged, _ = Diff([]Nameserver{{Hostname: "ns1"}}, current[:1])
	if got := hostnames(addedOrChanged); !slices.Equal(got, []string{"ns1"}) {
		t.Errorf("addedOrChanged = %v, want [ns1]", got)
	}
}

func TestDNSNameLists(t *testing.T) {
	t.Parallel()
	list := []string{"a.", "B"}

	if !containsDNSName(list, "a") || !containsDNSName(list, "b.") || containsDNSName(list, "c") {
		t.Error("containsDNSName mismatch")
	}

	if got := setDNSName(list, "c", true); !slices.Equal(got, []string{"a.", "B", "c"}) {
		t.Errorf("add = %v", got)
	}
	if got := setDNSName(list, "a", true); !slices.Equal(got, []string{"B", "a"}) {
		t.Errorf("re-add replaces existing spelling, got %v", got)
	}
	if got := setDNSName(list, "b.", false); !slices.Equal(got, []string{"a."}) {
		t.Errorf("remove = %v", got)
	}
	if got := setDNSName(nil, "x", false); len(got) != 0 {
		t.Errorf("remove from empty = %v", got)
	}
	if !slices.Equal(list, []string{"a.", "B"}) {
		t.Errorf("input list was modified: %v", list)
	}
}

func TestSOAModelFromRecord(t *testing.T) {
	t.Parallel()

	soa, err := soaModelFromRecord("ns1.example.com. hostmaster.example.com. 2024010101 10800 3600 604800 300", "example.com.", "hostmaster", true)
	if err != nil {
		t.Fatal(err)
	}
	want := SOAModel{
		RName:        types.StringValue("hostmaster"),
		Refresh:      types.Int64Value(10800),
		Retry:        types.Int64Value(3600),
		Expire:       types.Int64Value(604800),
		TTL:          types.Int64Value(300),
		CreateRecord: types.BoolValue(true),
	}
	if soa != want {
		t.Errorf("soa = %+v, want %+v", soa, want)
	}

	for _, bad := range []string{"ns1. host. 1 2 3", "ns1. host. 1 x 3 4 5", ""} {
		if _, err := soaModelFromRecord(bad, "example.com.", "hostmaster", true); err == nil {
			t.Errorf("soaModelFromRecord(%q) expected error", bad)
		}
	}

	rnames := []struct{ server, configured, want string }{
		// A fully qualified rname inside the zone keeps its spelling.
		{"hostmaster.example.com.", "hostmaster.example.com.", "hostmaster.example.com."},
		// Changed on the server: the server value shows the drift.
		{"admin.example.com.", "hostmaster.example.com.", "admin"},
		// Names that only contain the zone name are not mangled.
		{"hostmaster.example.com.other.org.", "hostmaster.example.com.other.org.", "hostmaster.example.com.other.org."},
		{"hostmaster.example.com.other.org.", "hostmaster", "hostmaster.example.com.other.org."},
	}
	for _, tt := range rnames {
		soa, err := soaModelFromRecord("ns1.example.com. "+tt.server+" 1 2 3 4 5", "example.com.", tt.configured, true)
		if err != nil {
			t.Fatal(err)
		}
		if got := soa.RName.ValueString(); got != tt.want {
			t.Errorf("rname of %q configured as %q = %q, want %q", tt.server, tt.configured, got, tt.want)
		}
	}
}

func TestReadName(t *testing.T) {
	t.Parallel()
	tests := []struct{ server, configured, want string }{
		{"ns1.example.com.", "ns1", "ns1"},
		{"ns1.example.com.", "ns1.example.com.", "ns1.example.com."},
		{"NS1.Example.com.", "ns1.example.com.", "ns1.example.com."},
		{"ns2.example.com.", "ns1.example.com.", "ns2"},
		{"ns2.example.com.", "", "ns2"},
		{"ns.other.org.", "ns.other.org.", "ns.other.org."},
		{"ns.other.org.", "ns1", "ns.other.org."},
		{"a.example.com.other.org.", "", "a.example.com.other.org."},
		{"ns1.notexample.com.", "", "ns1.notexample.com."},
		{"example.com.", "", "example.com."},
	}
	for _, tt := range tests {
		if got := readName(tt.server, tt.configured, "example.com."); got != tt.want {
			t.Errorf("readName(%q, %q) = %q, want %q", tt.server, tt.configured, got, tt.want)
		}
	}
}

func TestSOARrset(t *testing.T) {
	t.Parallel()
	soa := SOA{RName: "hostmaster", Refresh: 1, Retry: 2, Expire: 3, TTL: 4}

	rrset := soaRrset(soa, "ns1.example.com.", "example.com.", "2024010101")
	if rrset.Type != "SOA" || rrset.Name != "example.com." || rrset.Changetype != "" {
		t.Errorf("unexpected rrset: %+v", rrset)
	}
	if got := rrset.Records[0].Content; got != "ns1.example.com. hostmaster.example.com. 2024010101 1 2 3 4" {
		t.Errorf("content = %q", got)
	}

	soa.RName = "admin.other.org."
	if got := soaRrset(soa, "ns1.example.com.", "example.com.", "1").Records[0].Content; !strings.HasPrefix(got, "ns1.example.com. admin.other.org. ") {
		t.Errorf("fully qualified rname must be kept, got %q", got)
	}
}

func TestAddressRecordType(t *testing.T) {
	t.Parallel()
	tests := []struct {
		addr, want string
		ok         bool
	}{
		{"10.0.0.1", "A", true},
		{"2001:db8::1", "AAAA", true},
		{"::ffff:10.0.0.1", "A", true},
		{"nope", "", false},
	}
	for _, tt := range tests {
		got, ok := addressRecordType(tt.addr)
		if got != tt.want || ok != tt.ok {
			t.Errorf("addressRecordType(%q) = %q, %v; want %q, %v", tt.addr, got, ok, tt.want, tt.ok)
		}
	}
}

func TestNameserverRecordChanges(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	records, diags := nameserverRecordChanges(ctx,
		[]Nameserver{
			{Hostname: "ns1", Address: new("10.0.0.1")},
			{Hostname: "ns2"}, // no address: skipped
			{Hostname: "ns3.other.org.", Address: new("2001:db8::3")},
		},
		[]Nameserver{{Hostname: "old", Address: new("10.0.0.9")}},
		"example.com.",
	)
	if diags.HasError() {
		t.Fatal(diags)
	}

	want := []pdns_client.Rrset{
		{Type: "A", Changetype: "REPLACE", Name: "ns1.example.com.", Records: []pdns_client.Record{{Content: "10.0.0.1"}}},
		{Type: "AAAA", Changetype: "REPLACE", Name: "ns3.other.org.", Records: []pdns_client.Record{{Content: "2001:db8::3"}}},
		{Type: "A", Changetype: "DELETE", Name: "old.example.com."},
	}
	if fmt.Sprint(records) != fmt.Sprint(want) {
		t.Errorf("records =\n%+v\nwant\n%+v", records, want)
	}

	errorCases := map[string]struct{ added, deleted []Nameserver }{
		"invalid added address":   {added: []Nameserver{{Hostname: "ns1", Address: new("bad")}}},
		"invalid deleted address": {deleted: []Nameserver{{Hostname: "ns1", Address: new("bad")}}},
		"deleted without address": {deleted: []Nameserver{{Hostname: "ns1"}}},
	}
	for name, tc := range errorCases {
		if _, diags := nameserverRecordChanges(ctx, tc.added, tc.deleted, "example.com."); !diags.HasError() {
			t.Errorf("%s: expected error", name)
		}
	}
}

func TestReadNameservers(t *testing.T) {
	t.Parallel()
	zone := pdns_client.PDNSZone{
		Name: "example.com.",
		Rrsets: []pdns_client.Rrset{
			{Type: "A", Name: "ns1.example.com.", Records: []pdns_client.Record{{Content: "10.0.0.1"}}},
			{Type: "AAAA", Name: "ns2.example.com.", Records: []pdns_client.Record{{Content: "2001:db8::2"}}},
		},
	}

	list, diags := readNameservers(zone, []Nameserver{
		{Hostname: "ns1", Address: new("10.0.0.1")},
		{Hostname: "ns2.example.com.", Address: new("2001:db8::2")},
		{Hostname: "ns3"},
	})
	if diags.HasError() {
		t.Fatal(diags)
	}

	var got []Nameserver
	if diags := list.ElementsAs(context.Background(), &got, false); diags.HasError() {
		t.Fatal(diags)
	}
	// Hostnames keep their configured spelling.
	if len(got) != 3 || got[0].Hostname != "ns1" || *got[0].Address != "10.0.0.1" ||
		got[1].Hostname != "ns2.example.com." || *got[1].Address != "2001:db8::2" ||
		got[2].Hostname != "ns3" || got[2].Address != nil {
		t.Errorf("unexpected nameservers: %+v", got)
	}

	// A configured address without a matching record is an error.
	if _, diags := readNameservers(zone, []Nameserver{{Hostname: "ns1", Address: new("10.0.0.99")}}); !diags.HasError() {
		t.Error("expected error for missing address record")
	}
}

func zoneModel(t *testing.T, nameservers []Nameserver, soa SOAModel) ZoneResourceModel {
	t.Helper()
	ctx := context.Background()

	nsList, diags := types.ListValueFrom(ctx, types.ObjectType{AttrTypes: nameserverAttrTypes}, nameservers)
	if diags.HasError() {
		t.Fatal(diags)
	}
	soaObj, diags := types.ObjectValueFrom(ctx, soa.AttributeTypes(), soa)
	if diags.HasError() {
		t.Fatal(diags)
	}

	return ZoneResourceModel{
		Name:        types.StringValue("example.com."),
		Kind:        types.StringValue("Native"),
		Nameservers: nsList,
		SOA:         soaObj,
		Masters:     types.ListNull(types.StringType),
	}
}

func TestCreateZoneFromData(t *testing.T) {
	t.Parallel()
	soa := SOAModel{
		RName:        types.StringValue("hostmaster"),
		Refresh:      types.Int64Value(1),
		Retry:        types.Int64Value(2),
		Expire:       types.Int64Value(3),
		TTL:          types.Int64Value(4),
		CreateRecord: types.BoolValue(true),
	}
	data := zoneModel(t, []Nameserver{
		{Hostname: "ns1", Address: new("10.0.0.1")},
		{Hostname: "ns2.other.org.", Address: new("2001:db8::2")},
		{Hostname: "ns3"},
	}, soa)

	zone, serial, diags := createZoneFromData(context.Background(), data)
	if diags.HasError() {
		t.Fatal(diags)
	}

	if want := time.Now().Format("20060102") + "01"; serial != want {
		t.Errorf("serial = %q, want %q", serial, want)
	}
	if zone.Name != "example.com." || zone.Kind != "Native" {
		t.Errorf("unexpected zone: %+v", zone)
	}
	if !slices.Equal(zone.Nameservers, []string{"ns1.example.com.", "ns2.other.org.", "ns3.example.com."}) {
		t.Errorf("nameservers = %v", zone.Nameservers)
	}

	got := lo.Map(zone.Rrsets, func(r pdns_client.Rrset, _ int) string {
		return r.Type + " " + r.Name + " " + r.Records[0].Content
	})
	want := []string{
		"SOA example.com. ns1.example.com. hostmaster.example.com. " + serial + " 1 2 3 4",
		"A ns1.example.com. 10.0.0.1",
		"AAAA ns2.other.org. 2001:db8::2",
	}
	if !slices.Equal(got, want) {
		t.Errorf("rrsets =\n%v\nwant\n%v", got, want)
	}

	// Without create_record no SOA record and no serial.
	soa.CreateRecord = types.BoolValue(false)
	zone, serial, diags = createZoneFromData(context.Background(), zoneModel(t, []Nameserver{{Hostname: "ns1"}}, soa))
	if diags.HasError() {
		t.Fatal(diags)
	}
	if serial != "" || len(zone.Rrsets) != 0 {
		t.Errorf("expected no SOA and no serial, got serial %q rrsets %+v", serial, zone.Rrsets)
	}
}

func TestCreateZoneFromDataInvalidAddress(t *testing.T) {
	t.Parallel()
	soa := SOAModel{
		RName: types.StringValue("hostmaster"), Refresh: types.Int64Value(1), Retry: types.Int64Value(1),
		Expire: types.Int64Value(1), TTL: types.Int64Value(1), CreateRecord: types.BoolValue(true),
	}
	_, _, diags := createZoneFromData(context.Background(), zoneModel(t, []Nameserver{{Hostname: "ns1", Address: new("bad")}}, soa))
	if !diags.HasError() {
		t.Fatal("expected error for invalid address")
	}
}

func TestBase64Validator(t *testing.T) {
	t.Parallel()
	tests := []struct {
		value   types.String
		wantErr bool
	}{
		{types.StringValue("YWFhYQ=="), false},
		{types.StringValue("not base64!"), true},
		{types.StringValue("YWFhYQ"), true}, // missing padding
		{types.StringNull(), false},
		{types.StringUnknown(), false},
	}
	for _, tt := range tests {
		resp := &validator.StringResponse{}
		base64Validator{}.ValidateString(context.Background(), validator.StringRequest{Path: path.Root("key"), ConfigValue: tt.value}, resp)
		if resp.Diagnostics.HasError() != tt.wantErr {
			t.Errorf("value %s: error = %v, want %v", tt.value, resp.Diagnostics.HasError(), tt.wantErr)
		}
	}

	if (base64Validator{}).Description(context.Background()) == "" || (base64Validator{}).MarkdownDescription(context.Background()) == "" {
		t.Error("empty description")
	}
}

func TestZoneTSIGStatePurposes(t *testing.T) {
	t.Parallel()
	state := zoneTSIGState{
		masterIDs:      []string{"a."},
		slaveIDs:       []string{"B."},
		dnsupdateNames: []string{"a", "c"},
	}

	if got := state.purposesFor("a.", "a"); got != (tsigPurposes{DNSUpdate: true, AXFR: true}) {
		t.Errorf("a: %+v", got)
	}
	if got := state.purposesFor("b.", "b"); got != (tsigPurposes{Secondary: true}) {
		t.Errorf("b: %+v", got)
	}
	if got := state.purposesFor("x.", "x"); got.any() {
		t.Errorf("x: %+v", got)
	}
	if !(tsigPurposes{Secondary: true}).any() {
		t.Error("any() must be true with one purpose")
	}
}

func TestLockZone(t *testing.T) {
	t.Parallel()
	data := &PDNSProviderData{}

	// Spellings that differ by case or trailing dot share one lock.
	var inside, maxInside atomic.Int32
	var wg sync.WaitGroup
	for i := range 20 {
		wg.Go(func() {
			unlock := data.lockZone([]string{"Example.com.", "example.com"}[i%2])
			defer unlock()
			n := inside.Add(1)
			for {
				m := maxInside.Load()
				if n <= m || maxInside.CompareAndSwap(m, n) {
					break
				}
			}
			time.Sleep(time.Millisecond)
			inside.Add(-1)
		})
	}
	wg.Wait()

	if maxInside.Load() != 1 {
		t.Errorf("lock allowed %d concurrent holders", maxInside.Load())
	}

	// Different zones do not block each other.
	unlockA := data.lockZone("a.")
	done := make(chan struct{})
	go func() {
		data.lockZone("b.")()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Error("lock on b. blocked by lock on a.")
	}
	unlockA()
}

func TestSleepCtx(t *testing.T) {
	t.Parallel()
	if err := sleepCtx(context.Background(), time.Millisecond); err != nil {
		t.Errorf("sleepCtx = %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := sleepCtx(ctx, time.Hour); !errors.Is(err, context.Canceled) {
		t.Errorf("sleepCtx on cancelled ctx = %v", err)
	}
}
