package pdns_client

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/rand/v2"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/hashicorp/terraform-plugin-log/tflog"
)

type PDNSClient struct {
	httpClient *http.Client
	serverID   string
	apiKey     string
	endpoint   string

	// checkServer makes the first request verify that serverID exists at
	// endpoint, see WithServerCheck. serverMu guards serverChecked.
	checkServer   bool
	serverMu      sync.Mutex
	serverChecked bool

	// randInt64N and sleep are the random source and the wait of the PATCH
	// retry backoff, replaced in tests. nil means rand.Int64N and sleepCtx.
	randInt64N func(n int64) int64
	sleep      func(ctx context.Context, d time.Duration) error
}

// Option configures a PDNSClient.
type Option func(*PDNSClient)

// WithServerCheck makes the client verify, before its first request, that
// the server exists (GET /api/v1/servers/{server_id}). PowerDNS answers a
// request to an unknown server or a wrong API path with the same 404 as one
// for an unknown zone or TSIG key, so without the check a misconfigured
// endpoint or server_id looks like every object was deleted. A successful
// check is remembered; a failed one is repeated on the next request.
func WithServerCheck() Option {
	return func(c *PDNSClient) { c.checkServer = true }
}

// NewPDNSClient returns a client for the PowerDNS API at endpoint (the base
// URL, without /api/v1; a trailing slash is ignored).
func NewPDNSClient(httpClient *http.Client, endpoint string, serverID string, apiKey string, opts ...Option) *PDNSClient {
	client := &PDNSClient{
		httpClient: httpClient,
		serverID:   serverID,
		apiKey:     apiKey,
		endpoint:   strings.TrimRight(endpoint, "/"),
	}
	for _, opt := range opts {
		opt(client)
	}

	return client
}

// serverURL is the URL of the server resource, GET /api/v1/servers/{id}.
func (client *PDNSClient) serverURL() string {
	return client.endpoint + "/api/v1/servers/" + url.PathEscape(client.serverID)
}

// ensureServer verifies once that the configured server exists, see
// WithServerCheck.
func (client *PDNSClient) ensureServer(ctx context.Context) error {
	if !client.checkServer {
		return nil
	}
	client.serverMu.Lock()
	defer client.serverMu.Unlock()
	if client.serverChecked {
		return nil
	}

	req, err := client.newRequest(ctx, http.MethodGet, client.serverURL(), nil)
	if err != nil {
		return err
	}
	resp, err := client.httpClient.Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()

	switch resp.StatusCode {
	case http.StatusOK:
		_, _ = io.Copy(io.Discard, resp.Body)
		client.serverChecked = true
		return nil
	case http.StatusUnauthorized:
		return &PDNSUnauthorizedError{}
	case http.StatusNotFound:
		return &PDNSServerNotFoundError{ServerID: client.serverID, Endpoint: client.endpoint}
	default:
		data, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("checking server %q at %s: unexpected status code %d: %s", client.serverID, client.endpoint, resp.StatusCode, data)
	}
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
	Name       string   `json:"name,omitempty"`
	Type       string   `json:"type,omitempty"`
	Changetype string   `json:"changetype,omitempty"`
	Records    []Record `json:"records,omitempty"`
	// Comments is omitted when nil, which makes a REPLACE keep the comments
	// already on the server. A non-nil empty slice is sent as [] and removes
	// them.
	Comments []Comment `json:"comments,omitzero"`
	TTL      int64     `json:"ttl"`
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

// newRequest builds a request to the PDNS API with the headers every request
// carries. PowerDNS only reports the cause of an error, such as a database
// error in an HTTP 500, when the request asks for JSON: 4.9 and 5.0 need
// Accept: application/json, 5.1 an Accept or a JSON Content-Type header.
// Otherwise they answer a bare text/plain "Internal Server Error". Both
// headers are sent on every request, also on those without a body.
func (client *PDNSClient) newRequest(ctx context.Context, method, rawURL string, body io.Reader) (*http.Request, error) {
	req, err := http.NewRequestWithContext(ctx, method, rawURL, body)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	req.Header.Set("X-API-Key", client.apiKey)
	return req, nil
}

func (client *PDNSClient) getReq(ctx context.Context, method string, apiPath string, body io.Reader) (*http.Request, error) {
	req, err := client.newRequest(ctx, method, client.serverURL()+"/"+apiPath, body)
	if err != nil {
		return nil, err
	}

	headers := req.Header.Clone()
	headers.Del("X-API-Key")
	tflog.Debug(ctx, "Constructed HTTP Request for PDNS API", map[string]any{
		"url":     req.URL,
		"headers": headers,
		"method":  req.Method,
	})

	return req, nil
}

// do builds and executes a request against the PDNS API and validates the
// response status. It returns the response only when the status equals
// wantStatus; otherwise it maps well-known status codes to typed errors
// (PDNSUnauthorizedError, notFound on 404) or a generic error carrying the
// response body. Callers that get a non-nil response own closing its body.
func (client *PDNSClient) do(ctx context.Context, method, apiPath string, notFound error, body io.Reader, wantStatus int) (*http.Response, error) {
	if err := client.ensureServer(ctx); err != nil {
		return nil, err
	}

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
		return nil, &statusError{StatusCode: resp.StatusCode, Body: string(data)}
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

// patchDeadlockRetries is how often UpdateZoneRecords retries a PATCH that
// failed because of a database deadlock.
const patchDeadlockRetries = 4

// patchRetryBaseDelay is the mean wait before the first PATCH retry; the
// mean grows linearly with each retry, see patchRetryDelay.
const patchRetryBaseDelay = 100 * time.Millisecond

// patchRetryDelay returns the wait before PATCH retry number retry (1-based):
// a random duration in [retry*base/2, retry*base*3/2). The jitter spreads out
// the retries of writers that deadlocked together, which would otherwise
// retry at the same moment and deadlock again; the mean, and so the total
// time spent waiting, is that of a fixed retry*base backoff.
func (client *PDNSClient) patchRetryDelay(retry int) time.Duration {
	randInt64N := client.randInt64N
	if randInt64N == nil {
		randInt64N = rand.Int64N
	}
	mean := time.Duration(retry) * patchRetryBaseDelay
	return mean/2 + time.Duration(randInt64N(int64(mean)))
}

// sleepCtx waits for d, or until ctx is done, which it returns the error of.
func sleepCtx(ctx context.Context, d time.Duration) error {
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

// isPatchDeadlock reports whether a failed rrset PATCH looks like a gpgsql
// deadlock between concurrent PATCHes of the same zone. Because the client
// asks for JSON (see newRequest), PowerDNS includes the backend message in
// the error, and reports a deadlock as:
//
//   - the PostgreSQL message "deadlock detected" in the body, with HTTP 500
//     (deleting or inserting records, and on 4.9 and 5.0 rectifying) or 422
//     ("Failed to rectify ...", 5.1);
//   - an HTTP 500 with a GSQLBackend message, or an HTTP 422 "Failed to
//     rectify ..." with one, when the PostgreSQL message is localized;
//   - a bare HTTP 500 "Internal Server Error" when the server does not send
//     details anyway (4.9 and 5.0 only send them with Accept:
//     application/json, 5.1 with an Accept or a JSON Content-Type header).
//
// A GSQLBackend 500 is also what the server answers for other database
// errors, such as a value too long for its column. Retrying those is harmless
// (REPLACE and DELETE are idempotent) and only delays the error. Validation
// errors are a 422 with a different message and are not retried.
func isPatchDeadlock(err error) bool {
	se, ok := errors.AsType[*statusError](err)
	if !ok {
		return false
	}
	body := strings.TrimSpace(se.Body)
	switch {
	case strings.Contains(body, "deadlock detected"):
		return true
	case se.StatusCode == http.StatusInternalServerError:
		return strings.Contains(body, "GSQLBackend") || isGenericInternalServerError(body)
	case se.StatusCode == http.StatusUnprocessableEntity:
		return strings.Contains(body, "Failed to rectify") && strings.Contains(body, "GSQLBackend")
	}
	return false
}

// isGenericInternalServerError reports whether body is PowerDNS's generic 500
// body without details, as plain text or as a JSON error object.
func isGenericInternalServerError(body string) bool {
	const generic = "Internal Server Error"
	if body == generic {
		return true
	}
	var e struct {
		Error string `json:"error"`
	}
	return json.Unmarshal([]byte(body), &e) == nil && e.Error == generic
}

// UpdateZoneRecords applies rrset changes to a zone. The gpgsql backend fails
// concurrent PATCHes of the same zone with database deadlocks (see
// isPatchDeadlock); REPLACE and DELETE are idempotent, so such a PATCH is
// retried with a jittered backoff (see patchRetryDelay).
func (client *PDNSClient) UpdateZoneRecords(ctx context.Context, zoneID string, rrsets []Rrset) error {
	data, err := json.Marshal(PDNSZone{Rrsets: rrsets})
	if err != nil {
		return err
	}

	for attempt := 0; ; attempt++ {
		resp, err := client.do(ctx, http.MethodPatch, "zones/"+url.QueryEscape(zoneID), &PDNSZoneNotFoundError{ZoneID: zoneID}, bytes.NewReader(data), http.StatusNoContent)
		if err == nil {
			_ = resp.Body.Close()
			return nil
		}
		if attempt >= patchDeadlockRetries || !isPatchDeadlock(err) {
			return err
		}
		delay := client.patchRetryDelay(attempt + 1)
		tflog.Debug(ctx, "Retrying rrset PATCH after a database deadlock", map[string]any{"zone": zoneID, "attempt": attempt + 1, "delay": delay.String()})
		sleep := client.sleep
		if sleep == nil {
			sleep = sleepCtx
		}
		if err := sleep(ctx, delay); err != nil {
			return err
		}
	}
}
