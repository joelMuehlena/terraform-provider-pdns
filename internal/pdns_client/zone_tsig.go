package pdns_client

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/url"
)

// Metadata is a single zone metadata kind with its values.
type Metadata struct {
	Kind     string   `json:"kind"`
	Metadata []string `json:"metadata"`
}

func metadataPath(zoneID, kind string) string {
	return "zones/" + url.QueryEscape(zoneID) + "/metadata/" + url.PathEscape(kind)
}

// GetZoneMetadata returns the values of one metadata kind of a zone. A kind
// that is not set yields an empty list.
func (client *PDNSClient) GetZoneMetadata(ctx context.Context, zoneID, kind string) ([]string, error) {
	resp, err := client.do(ctx, http.MethodGet, metadataPath(zoneID, kind), &PDNSZoneNotFoundError{ZoneID: zoneID}, nil, http.StatusOK)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()

	var md Metadata
	if err := json.NewDecoder(resp.Body).Decode(&md); err != nil {
		return nil, err
	}
	return md.Metadata, nil
}

// SetZoneMetadata replaces all values of one metadata kind of a zone. Passing
// an empty list removes the kind.
func (client *PDNSClient) SetZoneMetadata(ctx context.Context, zoneID, kind string, values []string) error {
	if values == nil {
		values = []string{}
	}

	data, err := json.Marshal(Metadata{Kind: kind, Metadata: values})
	if err != nil {
		return err
	}

	resp, err := client.do(ctx, http.MethodPut, metadataPath(zoneID, kind), &PDNSZoneNotFoundError{ZoneID: zoneID}, bytes.NewReader(data), http.StatusOK)
	if err != nil {
		return err
	}
	_ = resp.Body.Close()
	return nil
}

// zoneTSIGUpdate is a zone PUT body carrying only the TSIG key lists. The
// pointers let an explicitly empty list be sent (to clear it), while nil
// leaves the list untouched.
type zoneTSIGUpdate struct {
	MasterTsigKeyIDs *[]string `json:"master_tsig_key_ids,omitempty"`
	SlaveTsigKeyIDs  *[]string `json:"slave_tsig_key_ids,omitempty"`
}

// UpdateZoneTSIGKeyIDs replaces the master (TSIG-ALLOW-AXFR) and/or slave
// (AXFR-MASTER-TSIG) TSIG key id lists of a zone. A nil argument leaves the
// corresponding list unchanged.
func (client *PDNSClient) UpdateZoneTSIGKeyIDs(ctx context.Context, zoneID string, master, slave *[]string) error {
	data, err := json.Marshal(zoneTSIGUpdate{MasterTsigKeyIDs: master, SlaveTsigKeyIDs: slave})
	if err != nil {
		return err
	}

	resp, err := client.do(ctx, http.MethodPut, "zones/"+url.QueryEscape(zoneID), &PDNSZoneNotFoundError{ZoneID: zoneID}, bytes.NewReader(data), http.StatusNoContent)
	if err != nil {
		return err
	}
	_ = resp.Body.Close()
	return nil
}
