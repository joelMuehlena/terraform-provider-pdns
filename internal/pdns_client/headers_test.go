package pdns_client

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
)

// TestRequestHeaders checks that every request, with or without a body,
// carries the API key, the User-Agent and JSON Content-Type and Accept
// headers. PowerDNS only includes the cause of an error in the response when
// the request has a JSON Content-Type.
const testUserAgent = "terraform-provider-pdns/1.2.3 (commit 0123456789ab) terraform/1.17.2"

func TestRequestHeaders(t *testing.T) {
	t.Parallel()

	type seen struct {
		method, path, contentType, accept, apiKey, userAgent string
	}
	var mu sync.Mutex
	var requests []seen
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.ReadAll(r.Body)
		mu.Lock()
		requests = append(requests, seen{r.Method, r.URL.Path, r.Header.Get("Content-Type"), r.Header.Get("Accept"), r.Header.Get("X-API-Key"), r.Header.Get("User-Agent")})
		mu.Unlock()
		// The status each call expects, so that no call fails.
		switch {
		case r.Method == http.MethodPost:
			w.WriteHeader(http.StatusCreated)
		case r.Method == http.MethodDelete, r.Method == http.MethodPatch,
			r.Method == http.MethodPut && r.URL.Path == "/api/v1/servers/localhost/zones/z.":
			w.WriteHeader(http.StatusNoContent)
			return
		}
		_, _ = io.WriteString(w, `{}`)
	}))
	t.Cleanup(srv.Close)
	client := NewPDNSClient(srv.Client(), srv.URL, "localhost", testAPIKey, WithServerCheck(), WithUserAgent(testUserAgent))
	ctx := context.Background()

	calls := map[string]func() error{
		"GetZone":    func() error { _, err := client.GetZone(ctx, "z.", true, ""); return err },
		"DeleteZone": func() error { return client.DeleteZone(ctx, "z.") },
		"CreateZone": func() error { _, err := client.CreateZone(ctx, PDNSZone{Name: "z."}); return err },
		"UpdateZone": func() error { return client.UpdateZone(ctx, "z.", PDNSZone{Account: "a"}) },
		"UpdateZoneRecords": func() error {
			return client.UpdateZoneRecords(ctx, "z.", []Rrset{{Name: "a.z.", Type: "A", Changetype: "DELETE"}})
		},
		"GetTSIGKey":      func() error { _, err := client.GetTSIGKey(ctx, "k."); return err },
		"CreateTSIGKey":   func() error { _, err := client.CreateTSIGKey(ctx, TSIGKey{Name: "k"}); return err },
		"UpdateTSIGKey":   func() error { _, err := client.UpdateTSIGKey(ctx, "k.", TSIGKey{Key: "x"}); return err },
		"DeleteTSIGKey":   func() error { return client.DeleteTSIGKey(ctx, "k.") },
		"GetZoneMetadata": func() error { _, err := client.GetZoneMetadata(ctx, "z.", "X-TEST"); return err },
		"SetZoneMetadata": func() error { return client.SetZoneMetadata(ctx, "z.", "X-TEST", []string{"v"}) },
	}
	for name, call := range calls {
		if err := call(); err != nil {
			t.Fatalf("%s: %v", name, err)
		}
	}

	methods := map[string]bool{}
	for _, r := range requests {
		methods[r.method] = true
		if r.contentType != "application/json" {
			t.Errorf("%s %s: Content-Type = %q, want application/json", r.method, r.path, r.contentType)
		}
		if r.accept != "application/json" {
			t.Errorf("%s %s: Accept = %q, want application/json", r.method, r.path, r.accept)
		}
		if r.userAgent != testUserAgent {
			t.Errorf("%s %s: User-Agent = %q, want %q", r.method, r.path, r.userAgent, testUserAgent)
		}
		if r.apiKey != testAPIKey {
			t.Errorf("%s %s: X-API-Key = %q, want %q", r.method, r.path, r.apiKey, testAPIKey)
		}
	}
	// The server check, then one request per call.
	if len(requests) != len(calls)+1 || requests[0].path != "/api/v1/servers/localhost" {
		t.Errorf("requests = %v, want the server check and %d calls", requests, len(calls))
	}
	for _, m := range []string{http.MethodGet, http.MethodPost, http.MethodPut, http.MethodPatch, http.MethodDelete} {
		if !methods[m] {
			t.Errorf("no %s request was checked", m)
		}
	}
}

// TestDefaultUserAgent checks that without WithUserAgent the client sends
// the default User-Agent of net/http.
func TestDefaultUserAgent(t *testing.T) {
	t.Parallel()
	userAgents := make(chan string, 1)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		userAgents <- r.Header.Get("User-Agent")
		_, _ = io.WriteString(w, `{}`)
	}))
	t.Cleanup(srv.Close)
	client := NewPDNSClient(srv.Client(), srv.URL, "localhost", testAPIKey)
	if _, err := client.GetZone(context.Background(), "z.", false, ""); err != nil {
		t.Fatal(err)
	}
	if got := <-userAgents; got != "Go-http-client/1.1" {
		t.Errorf("User-Agent = %q, want the net/http default", got)
	}
}
