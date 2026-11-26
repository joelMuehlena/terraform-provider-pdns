package provider

import (
	"context"
	"net/http"
	"slices"
	"strings"
	"testing"
	"time"

	"gitlab.com/joelMuehlena/homelab/code/terraform/provider/terraform-provider-pdns/internal/pdns_client"
)

// newTestProviderData returns provider data for direct calls, with its own
// zone locks (like a separate run) and production-like retry timing.
func newTestProviderData(fake *fakePDNS) *PDNSProviderData {
	return &PDNSProviderData{
		pdnsClient:      pdns_client.NewPDNSClient(fake.client, fake.srv.URL, "localhost", fakeAPIKey),
		lockRetryConfig: &applyRetry{maxWrites: 8, settle: 100 * time.Millisecond, backoff: jitteredBackoff},
	}
}

func dnsupdateList(fake *fakePDNS, zone string) []string {
	z, _ := fake.zone(zone)
	return normalizedNames(z.metadata[metadataTSIGAllowDNSUpdate])
}

func TestSetDNSUpdateLockSurvivesStaleWriter(t *testing.T) {
	t.Parallel()

	// Another run removed the last attachment from a list it read before the
	// lock was added, and writes [] (deleting the metadata) after our write:
	// either right after it, or while we wait for the settle re-check.
	for _, during := range []string{"after the lock write", "during the settle re-check"} {
		t.Run(during, func(t *testing.T) {
			t.Parallel()
			fake := newFakePDNS(t)
			fake.addZone("z.test.")
			fake.addKey("k1", secretA)
			fake.mutate(func(f *fakePDNS) { f.zones["z.test"].metadata[metadataTSIGAllowDNSUpdate] = []string{"k1"} })

			clobber := func(f *fakePDNS) { delete(f.zones["z.test"].metadata, metadataTSIGAllowDNSUpdate) }
			writes, readsAfterWrite, clobbered := 0, 0, false
			// Both hooks run with the fake's mutex held.
			fake.afterWrite = func(f *fakePDNS, r *http.Request) {
				writes++
				if during == "after the lock write" && !clobbered {
					clobbered = true
					clobber(f)
				}
			}
			fake.reject = func(r *http.Request) bool {
				if r.Method == http.MethodGet && writes == 1 && strings.Contains(r.URL.Path, "/metadata/") {
					readsAfterWrite++
					// The first read verifies the write; the second is the settle re-check.
					if during == "during the settle re-check" && readsAfterWrite == 2 && !clobbered {
						clobbered = true
						clobber(fake)
					}
				}
				return false
			}

			if err := newTestProviderData(fake).setDNSUpdateLock(context.Background(), "z.test.", true); err != nil {
				t.Fatal(err)
			}
			if !clobbered {
				t.Fatal("the stale write was never injected")
			}
			if got, want := dnsupdateList(fake, "z.test."), normalizedNames([]string{dnsupdateLockKey}); !slices.Equal(got, want) {
				t.Fatalf("dnsupdate list = %v, want %v", got, want)
			}
		})
	}
}
