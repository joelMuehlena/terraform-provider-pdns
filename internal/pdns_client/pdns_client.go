package pdns_client

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
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
	Rrsets           []Rrset  `json:"rrsets,omitempty"`
	Serial           int64    `json:"serial,omitempty"`
	NotifiedSerial   int64    `json:"notified_serial,omitempty"`
	EditedSerial     int64    `json:"edited_serial,omitempty"`
	Masters          []string `json:"masters,omitempty"`
	Dnssec           bool     `json:"dnssec,omitempty"`
	Nsec3Param       string   `json:"nsec3param,omitempty"`
	Nsec3Narrow      bool     `json:"nsec3narrow,omitempty"`
	Presigned        bool     `json:"presigned,omitempty"`
	SOAEdit          string   `json:"soa_edit,omitempty"`
	SOAEditAPI       string   `json:"soa_edit_api,omitempty"`
	APIRectify       bool     `json:"api_rectify,omitempty"`
	Zone             string   `json:"zone,omitempty"`
	Catalog          string   `json:"catalog,omitempty"`
	Account          string   `json:"account,omitempty"`
	Nameservers      []string `json:"nameservers,omitempty"`
	MasterTsigKeyIDS []string `json:"master_tsig_key_ids,omitempty"`
	SlaveTsigKeyIDS  []string `json:"slave_tsig_key_ids,omitempty"`
}

type Rrset struct {
	Name       string    `json:"name,omitempty"`
	Type       string    `json:"type,omitempty"`
	TTL        int64     `json:"ttl,omitempty"`
	Changetype string    `json:"changetype,omitempty"`
	Records    []Record  `json:"records,omitempty"`
	Comments   []Comment `json:"comments,omitempty"`
}

type Comment struct {
	Content    string `json:"content,omitempty"`
	Account    string `json:"account,omitempty"`
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

	req.Header.Add("X-API-Key", client.apiKey)
	fmt.Printf("Req: %v", req)

	return req, nil
}

func (client *PDNSClient) GetZone(ctx context.Context, zoneID string) (PDNSZone, error) {
	req, err := client.getReq(ctx, http.MethodGet, "zones/"+zoneID, nil)
	if err != nil {
		return PDNSZone{}, err
	}

	resp, err := client.httpClient.Do(req)
	if err != nil {
		return PDNSZone{}, err
	}

	if resp.StatusCode == http.StatusUnauthorized {
		return PDNSZone{}, &PDNSUnauthorizedError{}
	} else if resp.StatusCode == http.StatusNotFound {
		return PDNSZone{}, &PDNSZoneNotFoundError{
			ZoneID: zoneID,
		}
	} else if resp.StatusCode != http.StatusOK {
		return PDNSZone{}, fmt.Errorf("Unexpected code")
	}

	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return PDNSZone{}, err
	}

	var zone PDNSZone
	err = json.Unmarshal(data, &zone)
	if err != nil {
		return PDNSZone{}, err
	}

	return zone, nil
}

func (client *PDNSClient) DeleteZone(ctx context.Context, zoneID string) error {
	req, err := client.getReq(ctx, http.MethodDelete, "zones/"+zoneID, nil)
	if err != nil {
		return err
	}

	resp, err := client.httpClient.Do(req)
	if err != nil {
		return err
	}

	if resp.StatusCode == http.StatusUnauthorized {
		return &PDNSUnauthorizedError{}
	} else if resp.StatusCode != http.StatusNoContent {
		data, _ := io.ReadAll(resp.Body)

		return fmt.Errorf("Unexpected code %d, %s", resp.StatusCode, data)
	}

	return nil
}

func (client *PDNSClient) CreateZone(ctx context.Context, zone PDNSZone) (PDNSZone, error) {
	data, err := json.Marshal(zone)
	if err != nil {
		return PDNSZone{}, err
	}

	req, err := client.getReq(ctx, http.MethodPost, "zones", bytes.NewReader(data))
	if err != nil {
		return PDNSZone{}, err
	}

	resp, err := client.httpClient.Do(req)
	if err != nil {
		return PDNSZone{}, err
	}

	fmt.Printf("Res: %v", resp)

	if resp.StatusCode == http.StatusUnauthorized {
		return PDNSZone{}, &PDNSUnauthorizedError{}
	} else if resp.StatusCode != http.StatusCreated {
		data, _ := io.ReadAll(resp.Body)

		return PDNSZone{}, fmt.Errorf("Unexpected code %d, %s", resp.StatusCode, data)
	}

	return PDNSZone{}, nil
}
