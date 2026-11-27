package provider

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"

	"gitlab.com/joelMuehlena/homelab/code/terraform/provider/terraform-provider-pdns/internal/pdns_client"
)

const fakeAPIKey = "test-api-key"

// fakePDNS is an in-memory stand-in for the PowerDNS Authoritative HTTP API.
// It implements the endpoints the provider uses and mirrors the behavior
// observed on pdns-auth 5.1 where the provider depends on it.
type fakePDNS struct {
	t   *testing.T
	srv *httptest.Server
	// client keeps enough idle connections for many concurrent callers;
	// the default of 2 per host makes them open (and exhaust) new ports.
	client *http.Client

	mu    sync.Mutex
	zones map[string]*fakeZone
	keys  map[string]*pdns_client.TSIGKey

	// afterWrite, if set, runs (with mu held) after every successful write.
	afterWrite func(f *fakePDNS, r *http.Request)
	// reject, if set, makes requests it returns true for fail with 500.
	reject func(r *http.Request) bool
	// onPatch, if set, runs for every PATCH before the fake's mutex is taken,
	// so tests can observe concurrent requests.
	onPatch func(r *http.Request)
}

type fakeZone struct {
	name     string
	kind     string
	dnssec   bool
	serial   int64
	rrsets   []pdns_client.Rrset
	master   []string
	slave    []string
	metadata map[string][]string
	masters  []string
}

func newFakePDNS(t *testing.T) *fakePDNS {
	t.Helper()
	f := &fakePDNS{
		t:     t,
		zones: map[string]*fakeZone{},
		keys:  map[string]*pdns_client.TSIGKey{},
	}
	f.srv = httptest.NewServer(http.HandlerFunc(f.serveHTTP))
	t.Cleanup(f.srv.Close)

	transport := f.srv.Client().Transport.(*http.Transport).Clone()
	transport.MaxIdleConnsPerHost = 64
	f.client = &http.Client{Transport: transport}
	t.Cleanup(transport.CloseIdleConnections)
	return f
}

// providerConfig returns the provider block pointing at the fake server.
func (f *fakePDNS) providerConfig() string {
	return `
provider "pdns" {
  endpoint = "` + f.srv.URL + `"
  api_key  = "` + fakeAPIKey + `"
}
`
}

func fakeKey(name string) string { return strings.ToLower(strings.TrimSuffix(name, ".")) }

func writeJSON(w http.ResponseWriter, status int, v any) {
	// json.Marshal, not an Encoder: the Encoder's trailing newline is never
	// read by the client's decoder, so the connection could not be reused
	// and concurrent tests would exhaust local ports.
	data, _ := json.Marshal(v)
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_, _ = w.Write(data)
}

func writeError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]string{"error": msg})
}

func (f *fakePDNS) serveHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Header.Get("X-API-Key") != fakeAPIKey {
		writeError(w, http.StatusUnauthorized, "Unauthorized")
		return
	}

	if r.URL.EscapedPath() == "/api/v1/servers/localhost" && r.Method == http.MethodGet {
		writeJSON(w, http.StatusOK, map[string]string{"id": "localhost", "type": "Server", "daemon_type": "authoritative"})
		return
	}
	rest, ok := strings.CutPrefix(r.URL.EscapedPath(), "/api/v1/servers/localhost/")
	if !ok {
		writeError(w, http.StatusNotFound, "Not Found")
		return
	}
	segments := strings.Split(rest, "/")
	for i, s := range segments {
		if u, err := url.PathUnescape(s); err == nil {
			segments[i] = u
		}
	}

	if f.onPatch != nil && r.Method == http.MethodPatch {
		f.onPatch(r)
	}

	f.mu.Lock()
	defer f.mu.Unlock()

	if f.reject != nil && f.reject(r) {
		writeError(w, http.StatusInternalServerError, "rejected by test")
		return
	}

	written := false
	switch segments[0] {
	case "zones":
		written = f.serveZones(w, r, segments[1:])
	case "tsigkeys":
		written = f.serveTSIGKeys(w, r, segments[1:])
	default:
		writeError(w, http.StatusNotFound, "Not Found")
	}

	if written && f.afterWrite != nil {
		f.afterWrite(f, r)
	}
}

// serveZones handles /zones[/{id}[/metadata/{kind}]] and reports whether
// state was modified.
func (f *fakePDNS) serveZones(w http.ResponseWriter, r *http.Request, segments []string) bool {
	if len(segments) == 0 || segments[0] == "" {
		if r.Method == http.MethodPost {
			return f.createZone(w, r)
		}
		writeJSON(w, http.StatusOK, f.zoneList())
		return false
	}

	zone, ok := f.zones[fakeKey(segments[0])]
	if !ok {
		writeError(w, http.StatusNotFound, "Not Found")
		return false
	}

	if len(segments) == 3 && segments[1] == "metadata" {
		return f.serveMetadata(w, r, zone, segments[2])
	}

	switch r.Method {
	case http.MethodGet:
		writeJSON(w, http.StatusOK, zone.view(r.URL.Query()))
		return false
	case http.MethodPut:
		return f.updateZone(w, r, zone)
	case http.MethodPatch:
		return f.patchZone(w, r, zone)
	case http.MethodDelete:
		delete(f.zones, fakeKey(zone.name))
		w.WriteHeader(http.StatusNoContent)
		return true
	}
	writeError(w, http.StatusMethodNotAllowed, "Method not allowed")
	return false
}

func (f *fakePDNS) zoneList() []map[string]string {
	out := make([]map[string]string, 0, len(f.zones))
	for _, z := range f.zones {
		out = append(out, map[string]string{"id": z.name, "name": z.name})
	}
	return out
}

func (f *fakePDNS) createZone(w http.ResponseWriter, r *http.Request) bool {
	var in pdns_client.PDNSZone
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return false
	}
	if _, exists := f.zones[fakeKey(in.Name)]; exists {
		writeError(w, http.StatusConflict, "Conflict")
		return false
	}
	if rejectOutOfZone(w, in.Name, in.Rrsets) {
		return false
	}
	if len(in.Nameservers) > 0 && slices.ContainsFunc(in.Rrsets, func(rr pdns_client.Rrset) bool {
		return rr.Type == "NS" && fakeKey(rr.Name) == fakeKey(in.Name)
	}) {
		writeError(w, http.StatusUnprocessableEntity, "Nameservers list MUST NOT be mixed with zone-level NS in rrsets")
		return false
	}

	// Like PowerDNS, store the zone and owner names lowercased.
	in.Name = strings.ToLower(in.Name)
	zone := &fakeZone{name: in.Name, kind: in.Kind, dnssec: in.Dnssec, metadata: map[string][]string{}}
	for _, rr := range in.Rrsets {
		zone.rrsets = append(zone.rrsets, fakeStoreRrset(rr))
	}
	zone.setMasters(in.Masters)
	if _, ok := zone.rrset(in.Name, "SOA"); !ok {
		// Like PowerDNS, create a default SOA record if none is given.
		zone.rrsets = append(zone.rrsets, pdns_client.Rrset{Name: in.Name, Type: "SOA", TTL: 3600, Records: []pdns_client.Record{{
			Content: "a.misconfigured.dns.server.invalid. hostmaster." + in.Name + " 0 10800 3600 604800 3600",
		}}})
	}
	if len(in.Nameservers) > 0 {
		ns := pdns_client.Rrset{Name: in.Name, Type: "NS", TTL: 3600}
		for _, n := range in.Nameservers {
			ns.Records = append(ns.Records, pdns_client.Record{Content: n})
		}
		zone.rrsets = append(zone.rrsets, ns)
	}
	zone.updateSerial()
	f.zones[fakeKey(in.Name)] = zone

	writeJSON(w, http.StatusCreated, zone.view(nil))
	return true
}

func (f *fakePDNS) updateZone(w http.ResponseWriter, r *http.Request, zone *fakeZone) bool {
	var in map[string]json.RawMessage
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return false
	}

	lists := map[string]*[]string{"master_tsig_key_ids": &zone.master, "slave_tsig_key_ids": &zone.slave}
	updated := map[string][]string{}
	for field := range lists {
		raw, ok := in[field]
		if !ok {
			continue
		}
		var ids []string
		if err := json.Unmarshal(raw, &ids); err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return false
		}
		if field == "slave_tsig_key_ids" && len(ids) > 1 {
			writeError(w, http.StatusUnprocessableEntity, "Only one TSIG secondary key is currently allowed")
			return false
		}
		for _, id := range ids {
			if _, ok := f.keys[fakeKey(id)]; !ok {
				writeError(w, http.StatusUnprocessableEntity, "A TSIG key with the name '"+strings.TrimSuffix(id, ".")+"' does not exist")
				return false
			}
		}
		updated[field] = ids
	}

	for field, ids := range updated {
		*lists[field] = ids
	}
	if raw, ok := in["kind"]; ok {
		_ = json.Unmarshal(raw, &zone.kind)
	}
	if raw, ok := in["dnssec"]; ok {
		_ = json.Unmarshal(raw, &zone.dnssec)
	}
	zone.updateMasters(in)

	w.WriteHeader(http.StatusNoContent)
	return true
}

func (f *fakePDNS) patchZone(w http.ResponseWriter, r *http.Request, zone *fakeZone) bool {
	var in pdns_client.PDNSZone
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return false
	}
	if rejectOutOfZone(w, zone.name, in.Rrsets) {
		return false
	}

	for _, change := range in.Rrsets {
		change = fakeStoreRrset(change)
		zone.rrsets = slices.DeleteFunc(zone.rrsets, func(rr pdns_client.Rrset) bool {
			return rr.Name == change.Name && rr.Type == change.Type
		})
		switch change.Changetype {
		case "REPLACE":
			change.Changetype = ""
			zone.rrsets = append(zone.rrsets, change)
		case "DELETE":
		default:
			writeError(w, http.StatusUnprocessableEntity, "invalid changetype "+change.Changetype)
			return false
		}
	}
	zone.updateSerial()

	w.WriteHeader(http.StatusNoContent)
	return true
}

func (f *fakePDNS) serveMetadata(w http.ResponseWriter, r *http.Request, zone *fakeZone, kind string) bool {
	switch r.Method {
	case http.MethodGet:
		values := zone.metadata[kind]
		if values == nil {
			values = []string{}
		}
		writeJSON(w, http.StatusOK, pdns_client.Metadata{Kind: kind, Metadata: values})
		return false
	case http.MethodPut:
		var in pdns_client.Metadata
		if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return false
		}
		if len(in.Metadata) == 0 {
			delete(zone.metadata, kind)
		} else {
			zone.metadata[kind] = in.Metadata
		}
		writeJSON(w, http.StatusOK, in)
		return true
	}
	writeError(w, http.StatusMethodNotAllowed, "Method not allowed")
	return false
}

// serveTSIGKeys handles /tsigkeys[/{id}] and reports whether state was modified.
func (f *fakePDNS) serveTSIGKeys(w http.ResponseWriter, r *http.Request, segments []string) bool {
	if len(segments) == 0 || segments[0] == "" {
		if r.Method != http.MethodPost {
			writeError(w, http.StatusMethodNotAllowed, "Method not allowed")
			return false
		}
		var in pdns_client.TSIGKey
		if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return false
		}
		if in.Algorithm == "" {
			writeError(w, http.StatusUnprocessableEntity, "Key 'algorithm' not present or not a String")
			return false
		}
		if _, exists := f.keys[fakeKey(in.Name)]; exists {
			writeError(w, http.StatusConflict, "Conflict")
			return false
		}
		key := &pdns_client.TSIGKey{ID: fakeKey(in.Name) + ".", Name: in.Name, Algorithm: in.Algorithm, Key: in.Key, Type: "TSIGKey"}
		f.keys[fakeKey(in.Name)] = key
		writeJSON(w, http.StatusCreated, key)
		return true
	}

	key, ok := f.keys[fakeKey(segments[0])]
	if !ok {
		writeError(w, http.StatusNotFound, "TSIG key with name '"+segments[0]+"' not found")
		return false
	}

	switch r.Method {
	case http.MethodGet:
		writeJSON(w, http.StatusOK, key)
		return false
	case http.MethodPut:
		var in pdns_client.TSIGKey
		if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return false
		}
		if in.Key != "" {
			key.Key = in.Key
		}
		if in.Algorithm != "" {
			key.Algorithm = in.Algorithm
		}
		writeJSON(w, http.StatusOK, key)
		return true
	case http.MethodDelete:
		delete(f.keys, fakeKey(key.Name))
		w.WriteHeader(http.StatusNoContent)
		return true
	}
	writeError(w, http.StatusMethodNotAllowed, "Method not allowed")
	return false
}

// updateSerial sets the zone serial from its SOA record, like PowerDNS does.
func (z *fakeZone) updateSerial() {
	for _, rr := range z.rrsets {
		if rr.Type == "SOA" && len(rr.Records) > 0 {
			if fields := strings.Fields(rr.Records[0].Content); len(fields) >= 3 {
				z.serial, _ = strconv.ParseInt(fields[2], 10, 64)
			}
		}
	}
}

// view renders the zone as the API returns it, honoring the rrsets and
// rrset_name query parameters.
func (z *fakeZone) view(query url.Values) map[string]any {
	out := map[string]any{
		"id":                  z.name,
		"name":                z.name,
		"kind":                z.kind,
		"dnssec":              z.dnssec,
		"serial":              z.serial,
		"master_tsig_key_ids": nonNil(z.master),
		"slave_tsig_key_ids":  nonNil(z.slave),
		"masters":             nonNil(z.masters),
	}
	if query.Get("rrsets") == "false" {
		return out
	}

	rrsets := z.rrsets
	if name := query.Get("rrset_name"); name != "" {
		rrsets = slices.DeleteFunc(slices.Clone(rrsets), func(rr pdns_client.Rrset) bool { return fakeKey(rr.Name) != fakeKey(name) })
	}
	out["rrsets"] = nonNil(rrsets)
	return out
}

func nonNil[T any](s []T) []T {
	if s == nil {
		return []T{}
	}
	return s
}

// Accessors for tests. They lock the fake so they are safe to call between
// or during Terraform steps.

func (f *fakePDNS) zone(name string) (fakeZone, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	z, ok := f.zones[fakeKey(name)]
	if !ok {
		return fakeZone{}, false
	}
	cp := *z
	cp.rrsets = slices.Clone(z.rrsets)
	cp.master = slices.Clone(z.master)
	cp.slave = slices.Clone(z.slave)
	cp.masters = slices.Clone(z.masters)
	cp.metadata = map[string][]string{}
	for k, v := range z.metadata {
		cp.metadata[k] = slices.Clone(v)
	}
	return cp, true
}

func (f *fakePDNS) key(name string) (pdns_client.TSIGKey, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	k, ok := f.keys[fakeKey(name)]
	if !ok {
		return pdns_client.TSIGKey{}, false
	}
	return *k, true
}

func (f *fakePDNS) mutate(fn func(f *fakePDNS)) {
	f.mu.Lock()
	defer f.mu.Unlock()
	fn(f)
}

func (f *fakePDNS) addZone(name string) {
	f.mutate(func(f *fakePDNS) {
		f.zones[fakeKey(name)] = &fakeZone{name: name, kind: "Native", metadata: map[string][]string{}}
	})
}

func (f *fakePDNS) addKey(name, secret string) {
	f.mutate(func(f *fakePDNS) {
		f.keys[fakeKey(name)] = &pdns_client.TSIGKey{ID: fakeKey(name) + ".", Name: name, Algorithm: "hmac-sha256", Key: secret}
	})
}

func (z fakeZone) rrset(name, typ string) (pdns_client.Rrset, bool) {
	for _, rr := range z.rrsets {
		if rr.Name == name && rr.Type == typ {
			return rr, true
		}
	}
	return pdns_client.Rrset{}, false
}

// setMasters stores the primaries of a zone in the spelling PowerDNS uses.
func (z *fakeZone) setMasters(masters []string) {
	z.masters = nil
	for _, m := range masters {
		if c, ok := canonicalPrimary(m); ok {
			m = c
		}
		z.masters = append(z.masters, m)
	}
}

// updateMasters applies the masters of a zone PUT body, if given.
func (z *fakeZone) updateMasters(in map[string]json.RawMessage) {
	if raw, ok := in["masters"]; ok {
		var masters []string
		_ = json.Unmarshal(raw, &masters)
		z.setMasters(masters)
	}
}

// rejectOutOfZone fails the request with 422, like PowerDNS, if an rrset is
// not inside the zone, and reports whether it did.
func rejectOutOfZone(w http.ResponseWriter, zoneName string, rrsets []pdns_client.Rrset) bool {
	for _, rr := range rrsets {
		if !inZone(rr.Name, zoneName) {
			writeError(w, http.StatusUnprocessableEntity, "RRset "+rr.Name+" IN "+rr.Type+": Name is out of zone")
			return true
		}
	}
	return false
}
