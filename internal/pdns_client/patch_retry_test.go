package pdns_client

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

// newFlakyServer answers the first `fail` PATCH requests with a 500 carrying
// body, then 204.
func newFlakyServer(t *testing.T, fail int32, body string) (*PDNSClient, *atomic.Int32) {
	t.Helper()
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.ReadAll(r.Body)
		if calls.Add(1) <= fail {
			w.WriteHeader(http.StatusInternalServerError)
			_, _ = io.WriteString(w, body)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	t.Cleanup(srv.Close)
	return NewPDNSClient(srv.Client(), srv.URL, "localhost", testAPIKey), &calls
}

const deadlockBody = `{"error": "GSQLBackend unable to nullify ordername and update auth for z for domain_id 3: Fatal error during query: ... ERROR:  deadlock detected"}`

func TestUpdateZoneRecordsRetriesDeadlocks(t *testing.T) {
	t.Parallel()
	client, calls := newFlakyServer(t, 2, deadlockBody)

	if err := client.UpdateZoneRecords(context.Background(), "z.", []Rrset{{Name: "a.z.", Type: "A", Changetype: "DELETE"}}); err != nil {
		t.Fatalf("expected success after retries, got %v", err)
	}
	if got := calls.Load(); got != 3 {
		t.Errorf("requests = %d, want 3", got)
	}
}

func TestUpdateZoneRecordsGivesUpOnPersistentDeadlock(t *testing.T) {
	t.Parallel()
	client, calls := newFlakyServer(t, 100, deadlockBody)

	err := client.UpdateZoneRecords(context.Background(), "z.", nil)
	if err == nil || !strings.Contains(err.Error(), "deadlock detected") {
		t.Fatalf("expected the deadlock error, got %v", err)
	}
	if got := calls.Load(); got != patchDeadlockRetries+1 {
		t.Errorf("requests = %d, want %d", got, patchDeadlockRetries+1)
	}
}

func TestUpdateZoneRecordsDoesNotRetryOtherErrors(t *testing.T) {
	t.Parallel()
	client, calls := newFlakyServer(t, 100, `{"error": "something else"}`)

	if err := client.UpdateZoneRecords(context.Background(), "z.", nil); err == nil {
		t.Fatal("expected an error")
	}
	if got := calls.Load(); got != 1 {
		t.Errorf("requests = %d, want 1 (no retry)", got)
	}
}
