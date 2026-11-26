package pdns_client

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"

	"github.com/hashicorp/terraform-plugin-log/tflog"
)

type PDNSClient struct {
	httpClient *http.Client
	serverID   string
	apiKey     string
	endpoint   string
}

func NewPDNSClient(httpClient *http.Client, endpoint string, serverID string, apiKey string) *PDNSClient {
	client := &PDNSClient{
		httpClient: httpClient,
		serverID:   serverID,
		apiKey:     apiKey,
		endpoint:   endpoint,
	}

	return client
}

type PDNSZone struct {
	ID               string   `json:"id,omitempty"`
	Name             string   `json:"name,omitempty"`
	Type             string   `json:"type,omitempty"`
	URL              string   `json:"url,omitempty"`
	Kind             string   `json:"kind,omitempty"`
	Zone             string   `json:"zone,omitempty"`
	Catalog          string   `json:"catalog,omitempty"`
	Account          string   `json:"account,omitempty"`
	Nsec3Param       string   `json:"nsec3param,omitempty"`
	SOAEdit          string   `json:"soa_edit,omitempty"`
	SOAEditAPI       string   `json:"soa_edit_api,omitempty"`
	Rrsets           []Rrset  `json:"rrsets,omitempty"`
	Masters          []string `json:"masters,omitempty"`
	Nameservers      []string `json:"nameservers,omitempty"`
	MasterTsigKeyIDS []string `json:"master_tsig_key_ids,omitempty"`
	SlaveTsigKeyIDS  []string `json:"slave_tsig_key_ids,omitempty"`
	Serial           int64    `json:"serial,omitempty"`
	NotifiedSerial   int64    `json:"notified_serial,omitempty"`
	EditedSerial     int64    `json:"edited_serial,omitempty"`
	Dnssec           bool     `json:"dnssec,omitempty"`
	Nsec3Narrow      bool     `json:"nsec3narrow,omitempty"`
	Presigned        bool     `json:"presigned,omitempty"`
	APIRectify       bool     `json:"api_rectify,omitempty"`
}

type Rrset struct {
	Name       string    `json:"name,omitempty"`
	Type       string    `json:"type,omitempty"`
	Changetype string    `json:"changetype,omitempty"`
	Records    []Record  `json:"records,omitempty"`
	Comments   []Comment `json:"comments,omitempty"`
	TTL        int64     `json:"ttl"`
}

type Comment struct {
	Content string `json:"content,omitempty"`
	// Account is always sent: PowerDNS rejects comments without it (422 "Key
	// 'account' not present or not a String").
	Account    string `json:"account"`
	ModifiedAt int64  `json:"modified_at,omitempty"`
}

type Record struct {
	Content  string `json:"content,omitempty"`
	Disabled bool   `json:"disabled,omitempty"`
}

func (client *PDNSClient) getReq(ctx context.Context, method string, apiPath string, body io.Reader) (*http.Request, error) {
	req, err := http.NewRequestWithContext(
		ctx,
		method,
		client.endpoint+"/api/v1/servers/"+client.serverID+"/"+apiPath,
		body,
	)
	if err != nil {
		return nil, err
	}

	tflog.Debug(ctx, "Constructed HTTP Request for PDNS API", map[string]any{
		"url":     req.URL,
		"headers": req.Header,
		"method":  req.Method,
	})

	req.Header.Add("X-API-Key", client.apiKey)

	return req, nil
}

// do builds and executes a request against the PDNS API and validates the
// response status. It returns the response only when the status equals
// wantStatus; otherwise it maps well-known status codes to typed errors
// (PDNSUnauthorizedError, notFound on 404) or a generic error carrying the
// response body. Callers that get a non-nil response own closing its body.
func (client *PDNSClient) do(ctx context.Context, method, apiPath string, notFound error, body io.Reader, wantStatus int) (*http.Response, error) {
	req, err := client.getReq(ctx, method, apiPath, body)
	if err != nil {
		return nil, err
	}

	resp, err := client.httpClient.Do(req)
	if err != nil {
		return nil, err
	}

	if resp.StatusCode == wantStatus {
		return resp, nil
	}
	defer func() { _ = resp.Body.Close() }()

	switch resp.StatusCode {
	case http.StatusUnauthorized:
		return nil, &PDNSUnauthorizedError{}
	case http.StatusNotFound:
		return nil, notFound
	default:
		data, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("unexpected status code %d: %s", resp.StatusCode, data)
	}
}

func (client *PDNSClient) GetZone(ctx context.Context, zoneID string, withRrsets bool, limitToName string) (PDNSZone, error) {
	if limitToName != "" {
		limitToName = "&rrset_name=" + url.QueryEscape(limitToName)
	}

	apiPath := fmt.Sprintf("zones/%s?rrsets=%t%s", url.QueryEscape(zoneID), withRrsets, limitToName)
	resp, err := client.do(ctx, http.MethodGet, apiPath, &PDNSZoneNotFoundError{ZoneID: zoneID}, nil, http.StatusOK)
	if err != nil {
		return PDNSZone{}, err
	}
	defer func() { _ = resp.Body.Close() }()

	var zone PDNSZone
	if err := json.NewDecoder(resp.Body).Decode(&zone); err != nil {
		return PDNSZone{}, err
	}

	return zone, nil
}

func (client *PDNSClient) DeleteZone(ctx context.Context, zoneID string) error {
	resp, err := client.do(ctx, http.MethodDelete, "zones/"+url.QueryEscape(zoneID), &PDNSZoneNotFoundError{ZoneID: zoneID}, nil, http.StatusNoContent)
	if err != nil {
		return err
	}
	_ = resp.Body.Close()
	return nil
}

func (client *PDNSClient) CreateZone(ctx context.Context, zone PDNSZone) (PDNSZone, error) {
	data, err := json.Marshal(zone)
	if err != nil {
		return PDNSZone{}, err
	}

	resp, err := client.do(ctx, http.MethodPost, "zones", &PDNSZoneNotFoundError{ZoneID: zone.ID}, bytes.NewReader(data), http.StatusCreated)
	if err != nil {
		return PDNSZone{}, err
	}
	_ = resp.Body.Close()

	return PDNSZone{}, nil
}

func (client *PDNSClient) UpdateZone(ctx context.Context, zoneID string, zone PDNSZone) error {
	data, err := json.Marshal(zone)
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

// zoneSettingsUpdate is a zone PUT body for the kind and DNSSEC settings.
// Unlike PDNSZone it always carries dnssec, so it can be switched off.
type zoneSettingsUpdate struct {
	Kind   string `json:"kind,omitempty"`
	Dnssec bool   `json:"dnssec"`
}

// UpdateZoneSettings sets the kind (left unchanged if empty) and the DNSSEC
// state of a zone.
func (client *PDNSClient) UpdateZoneSettings(ctx context.Context, zoneID, kind string, dnssec bool) error {
	data, err := json.Marshal(zoneSettingsUpdate{Kind: kind, Dnssec: dnssec})
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

// UpdateZoneMasters replaces the primaries (masters) of a zone. nil or an
// empty list removes all of them.
func (client *PDNSClient) UpdateZoneMasters(ctx context.Context, zoneID string, masters []string) error {
	if masters == nil {
		masters = []string{}
	}
	data, err := json.Marshal(struct {
		Masters []string `json:"masters"`
	}{masters})
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

func (client *PDNSClient) UpdateZoneRecords(ctx context.Context, zoneID string, rrsets []Rrset) error {
	data, err := json.Marshal(PDNSZone{Rrsets: rrsets})
	if err != nil {
		return err
	}

	resp, err := client.do(ctx, http.MethodPatch, "zones/"+url.QueryEscape(zoneID), &PDNSZoneNotFoundError{ZoneID: zoneID}, bytes.NewReader(data), http.StatusNoContent)
	if err != nil {
		return err
	}
	_ = resp.Body.Close()
	return nil
}
