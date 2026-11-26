package pdns_client

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

const testAPIKey = "secret"

// recordedRequest is a request captured by the test server.
type recordedRequest struct {
	Method string
	Path   string // escaped path including the query
	Body   string
	APIKey string
}

// newTestClient starts a server that records every request and answers with
// the given status and body. It returns the client and the recorded requests.
func newTestClient(t *testing.T, status int, body string) (*PDNSClient, *[]recordedRequest) {
	t.Helper()

	var requests []recordedRequest
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		data, _ := io.ReadAll(r.Body)
		requests = append(requests, recordedRequest{
			Method: r.Method,
			Path:   r.URL.RequestURI(),
			Body:   string(data),
			APIKey: r.Header.Get("X-API-Key"),
		})
		w.WriteHeader(status)
		_, _ = io.WriteString(w, body)
	}))
	t.Cleanup(srv.Close)

	return NewPDNSClient(srv.Client(), srv.URL, "localhost", testAPIKey), &requests
}

func onlyRequest(t *testing.T, requests *[]recordedRequest) recordedRequest {
	t.Helper()
	if len(*requests) != 1 {
		t.Fatalf("expected exactly 1 request, got %d", len(*requests))
	}
	return (*requests)[0]
}

func assertJSON(t *testing.T, got, want string) {
	t.Helper()
	var g, w any
	if err := json.Unmarshal([]byte(got), &g); err != nil {
		t.Fatalf("request body is not JSON: %v (%q)", err, got)
	}
	if err := json.Unmarshal([]byte(want), &w); err != nil {
		t.Fatalf("invalid expected JSON: %v", err)
	}
	gb, _ := json.Marshal(g)
	wb, _ := json.Marshal(w)
	if string(gb) != string(wb) {
		t.Errorf("body mismatch\n got: %s\nwant: %s", gb, wb)
	}
}

func TestDoStatusMapping(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		status  int
		body    string
		wantErr func(error) bool
	}{
		{
			name:   "unauthorized",
			status: http.StatusUnauthorized,
			wantErr: func(err error) bool {
				var e *PDNSUnauthorizedError
				return errors.As(err, &e)
			},
		},
		{
			name:   "not found uses the given error",
			status: http.StatusNotFound,
			wantErr: func(err error) bool {
				var e *PDNSTSIGKeyNotFoundError
				return errors.As(err, &e) && e.KeyID == "k."
			},
		},
		{
			name:   "other status carries code and body",
			status: http.StatusUnprocessableEntity,
			body:   `{"error":"boom"}`,
			wantErr: func(err error) bool {
				return err != nil && strings.Contains(err.Error(), "422") && strings.Contains(err.Error(), "boom")
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			client, _ := newTestClient(t, tt.status, tt.body)

			_, err := client.GetTSIGKey(context.Background(), "k.")
			if !tt.wantErr(err) {
				t.Fatalf("unexpected error: %v", err)
			}
		})
	}
}

func TestRequestBasics(t *testing.T) {
	t.Parallel()
	client, requests := newTestClient(t, http.StatusOK, `{"name":"example.com."}`)

	if _, err := client.GetZone(context.Background(), "example.com.", true, ""); err != nil {
		t.Fatal(err)
	}

	req := onlyRequest(t, requests)
	if req.APIKey != testAPIKey {
		t.Errorf("X-API-Key = %q, want %q", req.APIKey, testAPIKey)
	}
	if req.Path != "/api/v1/servers/localhost/zones/example.com.?rrsets=true" {
		t.Errorf("path = %q", req.Path)
	}
}

func TestGetZone(t *testing.T) {
	t.Parallel()
	client, requests := newTestClient(t, http.StatusOK, `{
		"name": "example.com.",
		"kind": "Native",
		"serial": 2024010101,
		"master_tsig_key_ids": ["a."],
		"rrsets": [{"name": "www.example.com.", "type": "A", "ttl": 60, "records": [{"content": "10.0.0.1"}]}]
	}`)

	zone, err := client.GetZone(context.Background(), "example.com.", false, "www.example.com.")
	if err != nil {
		t.Fatal(err)
	}

	if got := onlyRequest(t, requests).Path; got != "/api/v1/servers/localhost/zones/example.com.?rrsets=false&rrset_name=www.example.com." {
		t.Errorf("path = %q", got)
	}
	if zone.Name != "example.com." || zone.Kind != "Native" || zone.Serial != 2024010101 {
		t.Errorf("unexpected zone: %+v", zone)
	}
	if len(zone.MasterTsigKeyIDS) != 1 || zone.MasterTsigKeyIDS[0] != "a." {
		t.Errorf("master_tsig_key_ids = %v", zone.MasterTsigKeyIDS)
	}
	if len(zone.Rrsets) != 1 || zone.Rrsets[0].Records[0].Content != "10.0.0.1" || zone.Rrsets[0].TTL != 60 {
		t.Errorf("rrsets = %+v", zone.Rrsets)
	}
}

func TestGetZoneNotFound(t *testing.T) {
	t.Parallel()
	client, _ := newTestClient(t, http.StatusNotFound, "")

	_, err := client.GetZone(context.Background(), "missing.", false, "")
	var e *PDNSZoneNotFoundError
	if !errors.As(err, &e) || e.ZoneID != "missing." {
		t.Fatalf("expected PDNSZoneNotFoundError for missing., got %v", err)
	}
}

func TestGetZoneInvalidJSON(t *testing.T) {
	t.Parallel()
	client, _ := newTestClient(t, http.StatusOK, "not json")

	if _, err := client.GetZone(context.Background(), "example.com.", false, ""); err == nil {
		t.Fatal("expected decode error")
	}
}

func TestZoneWrites(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		status     int
		call       func(*PDNSClient) error
		wantMethod string
		wantPath   string
		wantBody   string
	}{
		{
			name:   "create",
			status: http.StatusCreated,
			call: func(c *PDNSClient) error {
				_, err := c.CreateZone(context.Background(), PDNSZone{Name: "example.com.", Kind: "Native", Nameservers: []string{"ns1.example.com."}})
				return err
			},
			wantMethod: http.MethodPost,
			wantPath:   "/api/v1/servers/localhost/zones",
			wantBody:   `{"name":"example.com.","kind":"Native","nameservers":["ns1.example.com."]}`,
		},
		{
			name:   "update",
			status: http.StatusNoContent,
			call: func(c *PDNSClient) error {
				return c.UpdateZone(context.Background(), "example.com.", PDNSZone{Kind: "Master", Dnssec: true})
			},
			wantMethod: http.MethodPut,
			wantPath:   "/api/v1/servers/localhost/zones/example.com.",
			wantBody:   `{"kind":"Master","dnssec":true}`,
		},
		{
			name:   "update settings",
			status: http.StatusNoContent,
			call: func(c *PDNSClient) error {
				return c.UpdateZoneSettings(context.Background(), "example.com.", "Native", false)
			},
			wantMethod: http.MethodPut,
			wantPath:   "/api/v1/servers/localhost/zones/example.com.",
			wantBody:   `{"kind":"Native","dnssec":false}`,
		},
		{
			name:   "update records with comment",
			status: http.StatusNoContent,
			call: func(c *PDNSClient) error {
				return c.UpdateZoneRecords(context.Background(), "example.com.", []Rrset{{
					Name: "www.example.com.", Type: "A", Changetype: "REPLACE", TTL: 60,
					Records:  []Record{{Content: "10.0.0.1"}},
					Comments: []Comment{{Content: "hi"}},
				}})
			},
			wantMethod: http.MethodPatch,
			wantPath:   "/api/v1/servers/localhost/zones/example.com.",
			wantBody:   `{"rrsets":[{"name":"www.example.com.","type":"A","changetype":"REPLACE","records":[{"content":"10.0.0.1"}],"comments":[{"content":"hi","account":""}],"ttl":60}]}`,
		},
		{
			name:   "update records",
			status: http.StatusNoContent,
			call: func(c *PDNSClient) error {
				return c.UpdateZoneRecords(context.Background(), "example.com.", []Rrset{{
					Name: "www.example.com.", Type: "A", Changetype: "REPLACE", TTL: 60,
					Records: []Record{{Content: "10.0.0.1"}},
				}})
			},
			wantMethod: http.MethodPatch,
			wantPath:   "/api/v1/servers/localhost/zones/example.com.",
			wantBody:   `{"rrsets":[{"name":"www.example.com.","type":"A","changetype":"REPLACE","records":[{"content":"10.0.0.1"}],"ttl":60}]}`,
		},
		{
			name:   "delete",
			status: http.StatusNoContent,
			call: func(c *PDNSClient) error {
				return c.DeleteZone(context.Background(), "example.com.")
			},
			wantMethod: http.MethodDelete,
			wantPath:   "/api/v1/servers/localhost/zones/example.com.",
		},
		{
			name:   "set tsig key ids clears with empty list and omits nil",
			status: http.StatusNoContent,
			call: func(c *PDNSClient) error {
				return c.UpdateZoneTSIGKeyIDs(context.Background(), "example.com.", &[]string{}, nil)
			},
			wantMethod: http.MethodPut,
			wantPath:   "/api/v1/servers/localhost/zones/example.com.",
			wantBody:   `{"master_tsig_key_ids":[]}`,
		},
		{
			name:   "set both tsig key id lists",
			status: http.StatusNoContent,
			call: func(c *PDNSClient) error {
				return c.UpdateZoneTSIGKeyIDs(context.Background(), "example.com.", &[]string{"a."}, &[]string{"b."})
			},
			wantMethod: http.MethodPut,
			wantPath:   "/api/v1/servers/localhost/zones/example.com.",
			wantBody:   `{"master_tsig_key_ids":["a."],"slave_tsig_key_ids":["b."]}`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			client, requests := newTestClient(t, tt.status, "")

			if err := tt.call(client); err != nil {
				t.Fatal(err)
			}

			req := onlyRequest(t, requests)
			if req.Method != tt.wantMethod || req.Path != tt.wantPath {
				t.Errorf("request = %s %s, want %s %s", req.Method, req.Path, tt.wantMethod, tt.wantPath)
			}
			if tt.wantBody != "" {
				assertJSON(t, req.Body, tt.wantBody)
			}
		})
	}
}

func TestZoneWritesWrongStatus(t *testing.T) {
	t.Parallel()
	// A write that expects 204 must fail on 200.
	client, _ := newTestClient(t, http.StatusOK, "")

	if err := client.DeleteZone(context.Background(), "example.com."); err == nil {
		t.Fatal("expected error for unexpected status")
	}
}

func TestZoneIDIsEscaped(t *testing.T) {
	t.Parallel()
	client, requests := newTestClient(t, http.StatusNoContent, "")

	if err := client.DeleteZone(context.Background(), "a/b.example."); err != nil {
		t.Fatal(err)
	}
	if got := onlyRequest(t, requests).Path; got != "/api/v1/servers/localhost/zones/a%2Fb.example." {
		t.Errorf("path = %q", got)
	}
}
