package pdns_client

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// newFlakyServer answers the first `fail` PATCH requests with status and
// body, then 204. The client does not wait between retries.
func newFlakyServer(t *testing.T, fail int32, status int, body string) (*PDNSClient, *atomic.Int32) {
	t.Helper()
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.ReadAll(r.Body)
		if calls.Add(1) <= fail {
			w.WriteHeader(status)
			_, _ = io.WriteString(w, body)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	t.Cleanup(srv.Close)
	client := NewPDNSClient(srv.Client(), srv.URL, "localhost", testAPIKey)
	client.sleep = func(context.Context, time.Duration) error { return nil }
	return client, &calls
}

// Responses of PowerDNS with the gpgsql backend to concurrent PATCHes of one
// zone, recorded from the servers of the acceptance tests.
const (
	// HTTP 500 (4.9, 5.0, 5.1).
	deadlockBody = `{"error": "GSQLBackend unable to delete RRSet raw.test|SOA: Fatal error during query: delete from records where domain_id=$1 and name=$2 and type=$3: ERROR:  deadlock detected\nDETAIL:  Process 322 waits for AccessExclusiveLock on tuple (24,25) of relation 16400 of database 16384; blocked by process 320.\nProcess 320 waits for ShareLock on transaction 984; blocked by process 319.\nProcess 319 waits for ShareLock on transaction 982; blocked by process 318.\nProcess 318 waits for ExclusiveLock on tuple (24,25) of relation 16400 of database 16384; blocked by process 322.\nHINT:  See server log for query details.\n"}`
	// 4.9 and 5.0, HTTP 500.
	rectifyDeadlock500Body = `{"error": "GSQLBackend unable to nullify ordername and update auth for raw.test for domain_id 5: Fatal error during query: update records set ordername=NULL,auth=$1 where domain_id=$2 and name=$3 and disabled=false: ERROR:  deadlock detected\nDETAIL:  Process 854 waits for ShareLock on transaction 1269; blocked by process 860.\nProcess 860 waits for ShareLock on transaction 1267; blocked by process 854.\nHINT:  See server log for query details.\nCONTEXT:  while updating tuple (10,5) in relation \"records\"\n"}`
	// 5.1, HTTP 422.
	rectifyDeadlockBody = `{"error": "Failed to rectify 'raw.test.' Exception while rectifying zone 'raw.test': GSQLBackend unable to nullify ordername and update auth for raw.test for domain_id 2: Fatal error during query: update records set ordername=NULL,auth=$1 where domain_id=$2 and name=$3 and disabled=false: ERROR:  deadlock detected\nDETAIL:  Process 312 waits for ShareLock on transaction 982; blocked by process 318.\nProcess 318 waits for ShareLock on transaction 980; blocked by process 312.\nHINT:  See server log for query details.\nCONTEXT:  while updating tuple (25,7) in relation \"records\"\n"}`
	// Without Accept: application/json (4.9, 5.0) or any Accept or JSON
	// Content-Type header (5.1), HTTP 500 text/plain.
	bareInternalServerErrorBody = "Internal Server Error"
)

func TestUpdateZoneRecordsRetriesDeadlocks(t *testing.T) {
	t.Parallel()
	for name, tc := range map[string]struct {
		status int
		body   string
	}{
		"deadlock detected":     {http.StatusInternalServerError, deadlockBody},
		"rectify deadlock":      {http.StatusUnprocessableEntity, rectifyDeadlockBody},
		"rectify deadlock, 500": {http.StatusInternalServerError, rectifyDeadlock500Body},
		"bare 500":              {http.StatusInternalServerError, bareInternalServerErrorBody},
		"bare 500 with newline": {http.StatusInternalServerError, bareInternalServerErrorBody + "\n"},
		"generic JSON 500":      {http.StatusInternalServerError, `{"error": "Internal Server Error"}`},
		// The PostgreSQL message is localized with lc_messages.
		"deadlock, localized": {http.StatusInternalServerError, `{"error": "GSQLBackend unable to delete RRSet raw.test|SOA: Fatal error during query: delete from records where domain_id=$1 and name=$2 and type=$3: FEHLER:  Verklemmung (Deadlock) entdeckt\n"}`},
		// Other database errors are retried as well; see isPatchDeadlock.
		"value too long":              {http.StatusInternalServerError, `{"error": "GSQLBackend unable to feed record t.z|TXT: Fatal error during query: insert into records (content,ttl,prio,type,domain_id,disabled,name,ordername,auth) values ($1,$2,$3,$4,$5,$6,$7,$8,$9): ERROR:  value too long for type character varying(65535)\n"}`},
		"rectify deadlock, localized": {http.StatusUnprocessableEntity, `{"error": "Failed to rectify 'raw.test.' Exception while rectifying zone 'raw.test': GSQLBackend unable to nullify ordername and update auth for raw.test for domain_id 2: Fatal error during query: update records set ordername=NULL,auth=$1 where domain_id=$2 and name=$3 and disabled=false: FEHLER:  Verklemmung (Deadlock) entdeckt\n"}`},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			client, calls := newFlakyServer(t, 2, tc.status, tc.body)

			if err := client.UpdateZoneRecords(context.Background(), "z.", []Rrset{{Name: "a.z.", Type: "A", Changetype: "DELETE"}}); err != nil {
				t.Fatalf("expected success after retries, got %v", err)
			}
			if got := calls.Load(); got != 3 {
				t.Errorf("requests = %d, want 3", got)
			}
		})
	}
}

func TestUpdateZoneRecordsGivesUpOnPersistentDeadlock(t *testing.T) {
	t.Parallel()
	client, calls := newFlakyServer(t, 100, http.StatusInternalServerError, deadlockBody)

	err := client.UpdateZoneRecords(context.Background(), "z.", nil)
	if err == nil || !strings.Contains(err.Error(), "unexpected status code 500: ") || !strings.Contains(err.Error(), "deadlock detected") {
		t.Fatalf("expected the deadlock error, got %v", err)
	}
	if got := calls.Load(); got != patchDeadlockRetries+1 {
		t.Errorf("requests = %d, want %d", got, patchDeadlockRetries+1)
	}
}

func TestUpdateZoneRecordsDoesNotRetryOtherErrors(t *testing.T) {
	t.Parallel()
	for name, tc := range map[string]struct {
		status int
		body   string
	}{
		// Validation errors, recorded from PowerDNS 5.1.
		"invalid content":   {http.StatusUnprocessableEntity, `{"error": "Record a.z./A 'not-an-ip': Parsing record content (try 'pdnsutil zone check'): while parsing IP address, expected digits at position 0 in 'not-an-ip'"}`},
		"out of zone":       {http.StatusUnprocessableEntity, `{"error": "RRset a.other.test. IN A: Name is out of zone"}`},
		"conflicting rrset": {http.StatusUnprocessableEntity, `{"error": "RRset c.z. IN A: Conflicts with pre-existing RRset"}`},
		"duplicate record":  {http.StatusUnprocessableEntity, `{"error": "RRset x.z. IN A: duplicate record with content \"1.2.3.4\""}`},
		"bad request":       {http.StatusBadRequest, `{"error": "Bad Request"}`},
		// Rectify failures without a backend error are not deadlocks.
		"rectify, no backend error": {http.StatusUnprocessableEntity, `{"error": "Failed to rectify 'z.' something else"}`},
		"other 500":                 {http.StatusInternalServerError, `{"error": "something else"}`},
		"bare 503":                  {http.StatusServiceUnavailable, "Internal Server Error"},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			client, calls := newFlakyServer(t, 100, tc.status, tc.body)

			if err := client.UpdateZoneRecords(context.Background(), "z.", nil); err == nil {
				t.Fatal("expected an error")
			}
			if got := calls.Load(); got != 1 {
				t.Errorf("requests = %d, want 1 (no retry)", got)
			}
		})
	}
}

func TestPatchRetryDelay(t *testing.T) {
	t.Parallel()
	client := &PDNSClient{}
	for retry := 1; retry <= patchDeadlockRetries; retry++ {
		mean := time.Duration(retry) * patchRetryBaseDelay
		client.randInt64N = func(int64) int64 { return 0 }
		if got := client.patchRetryDelay(retry); got != mean/2 {
			t.Errorf("retry %d, smallest delay = %v, want %v", retry, got, mean/2)
		}
		client.randInt64N = func(n int64) int64 { return n - 1 }
		if got := client.patchRetryDelay(retry); got != mean*3/2-1 {
			t.Errorf("retry %d, largest delay = %v, want %v", retry, got, mean*3/2-1)
		}
	}

	// The default random source stays in range and does not repeat itself.
	client.randInt64N = nil
	seen := map[time.Duration]bool{}
	for range 100 {
		d := client.patchRetryDelay(2)
		if d < patchRetryBaseDelay || d >= 3*patchRetryBaseDelay {
			t.Fatalf("delay %v out of [%v, %v)", d, patchRetryBaseDelay, 3*patchRetryBaseDelay)
		}
		seen[d] = true
	}
	if len(seen) < 50 {
		t.Errorf("only %d distinct delays out of 100", len(seen))
	}
}

func TestUpdateZoneRecordsBackoff(t *testing.T) {
	t.Parallel()
	client, _ := newFlakyServer(t, 100, http.StatusInternalServerError, deadlockBody)
	var delays []time.Duration
	client.sleep = func(_ context.Context, d time.Duration) error {
		delays = append(delays, d)
		return nil
	}
	client.randInt64N = func(n int64) int64 { return n / 2 }

	if err := client.UpdateZoneRecords(context.Background(), "z.", nil); err == nil {
		t.Fatal("expected an error")
	}
	want := []time.Duration{100 * time.Millisecond, 200 * time.Millisecond, 300 * time.Millisecond, 400 * time.Millisecond}
	if !slices.Equal(delays, want) {
		t.Errorf("delays = %v, want %v", delays, want)
	}
}

func TestUpdateZoneRecordsStopsWaitingWhenCanceled(t *testing.T) {
	t.Parallel()
	client, calls := newFlakyServer(t, 100, http.StatusInternalServerError, deadlockBody)
	// The real wait, of 150ms before the first retry.
	client.sleep = nil
	client.randInt64N = func(n int64) int64 { return n - 1 }

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	start := time.Now()
	err := client.UpdateZoneRecords(ctx, "z.", nil)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("expected the context error, got %v", err)
	}
	if elapsed := time.Since(start); elapsed > 120*time.Millisecond {
		t.Errorf("returned after %v, want right after the context was done", elapsed)
	}
	if got := calls.Load(); got != 1 {
		t.Errorf("requests = %d, want 1", got)
	}
}
