package pdns_client

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
)

// newServerCheckClient starts a server whose GET /api/v1/servers/{id}
// answers with serverStatus (200 for "localhost" if 0) and that answers every
// other request with 200 "{}". It returns a client with WithServerCheck and
// the paths requested.
func newServerCheckClient(t *testing.T, endpointSuffix, serverID string, serverStatus *atomic.Int32) (*PDNSClient, func() []string) {
	t.Helper()
	var mu sync.Mutex
	var paths []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		paths = append(paths, r.URL.Path)
		mu.Unlock()
		if strings.Count(r.URL.Path, "/") == 4 {
			status := int(serverStatus.Load())
			if status == 0 {
				status = http.StatusNotFound
				if r.URL.Path == "/api/v1/servers/localhost" {
					status = http.StatusOK
				}
			}
			w.WriteHeader(status)
			_, _ = w.Write([]byte(`{"id":"localhost"}`))
			return
		}
		if !strings.HasPrefix(r.URL.Path, "/api/v1/servers/localhost/") {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		_, _ = w.Write([]byte(`{}`))
	}))
	t.Cleanup(srv.Close)
	client := NewPDNSClient(srv.Client(), srv.URL+endpointSuffix, serverID, testAPIKey, WithServerCheck())
	return client, func() []string {
		mu.Lock()
		defer mu.Unlock()
		return append([]string(nil), paths...)
	}
}

func TestServerCheckRunsOnce(t *testing.T) {
	t.Parallel()
	client, paths := newServerCheckClient(t, "/", "localhost", &atomic.Int32{})

	var wg sync.WaitGroup
	for range 5 {
		wg.Go(func() {
			if _, err := client.GetZone(context.Background(), "z.", false, ""); err != nil {
				t.Error(err)
			}
		})
	}
	wg.Wait()

	checks := 0
	for _, p := range paths() {
		if strings.Contains(p, "//") {
			t.Errorf("path %q has a double slash: the trailing slash of the endpoint was not trimmed", p)
		}
		if p == "/api/v1/servers/localhost" {
			checks++
		}
	}
	if checks != 1 || len(paths()) != 6 {
		t.Errorf("requests = %q, want one server check and 5 zone requests", paths())
	}
}

func TestServerCheckUnknownServer(t *testing.T) {
	t.Parallel()
	client, paths := newServerCheckClient(t, "", "wrong", &atomic.Int32{})

	for range 2 {
		_, err := client.GetZone(context.Background(), "z.", false, "")
		var e *PDNSServerNotFoundError
		if !errors.As(err, &e) || e.ServerID != "wrong" {
			t.Fatalf("expected PDNSServerNotFoundError, got %v", err)
		}
		if !strings.Contains(err.Error(), `server_id "wrong" not found at endpoint http://`) {
			t.Errorf("error = %q", err)
		}
		if _, ok := errors.AsType[*PDNSZoneNotFoundError](err); ok {
			t.Error("a missing server must not look like a missing zone")
		}
	}
	// A failed check is not remembered, and the zone is never requested.
	if got := paths(); len(got) != 2 || got[0] != "/api/v1/servers/wrong" || got[1] != got[0] {
		t.Errorf("requests = %q", got)
	}
}

func TestServerCheckErrors(t *testing.T) {
	t.Parallel()
	status := &atomic.Int32{}
	client, paths := newServerCheckClient(t, "", "localhost", status)

	status.Store(http.StatusUnauthorized)
	_, err := client.GetZone(context.Background(), "z.", false, "")
	if _, ok := errors.AsType[*PDNSUnauthorizedError](err); !ok {
		t.Fatalf("expected PDNSUnauthorizedError, got %v", err)
	}

	status.Store(http.StatusInternalServerError)
	if _, err := client.GetZone(context.Background(), "z.", false, ""); err == nil || !strings.Contains(err.Error(), "500") {
		t.Fatalf("expected a 500 error, got %v", err)
	}

	// Once the server answers, the check passes and is not repeated.
	status.Store(http.StatusOK)
	for range 2 {
		if _, err := client.GetZone(context.Background(), "z.", false, ""); err != nil {
			t.Fatal(err)
		}
	}
	if got := len(paths()); got != 5 {
		t.Errorf("requests = %q, want 3 checks and 2 zone requests", paths())
	}
}

func TestNoServerCheckByDefault(t *testing.T) {
	t.Parallel()
	client, requests := newTestClient(t, http.StatusOK, `{}`)
	if _, err := client.GetZone(context.Background(), "z.", false, ""); err != nil {
		t.Fatal(err)
	}
	if got := onlyRequest(t, requests).Path; got != "/api/v1/servers/localhost/zones/z.?rrsets=false" {
		t.Errorf("path = %q", got)
	}
}
