package provider

import (
	"net/http"
	"net/http/httptest"
	"regexp"
	"sync"
	"testing"

	tfresource "github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"gitlab.com/joelMuehlena/homelab/code/terraform/provider/terraform-provider-pdns/internal/version"
)

func TestUserAgent(t *testing.T) {
	t.Parallel()
	tests := []struct {
		build            version.Info
		terraformVersion string
		want             string
	}{
		{testBuild, "1.17.2", "terraform-provider-pdns/test (commit 0123456789ab) terraform/1.17.2"},
		{version.Info{Version: "dev", Commit: "unknown"}, "", "terraform-provider-pdns/dev (commit unknown)"},
		{version.Info{Version: "1.2.3", Commit: "abc1234", Modified: true}, "1.13.10", "terraform-provider-pdns/1.2.3 (commit abc1234-dirty) terraform/1.13.10"},
	}
	for _, tt := range tests {
		if got := userAgent(tt.build, tt.terraformVersion); got != tt.want {
			t.Errorf("userAgent(%+v, %q) = %q, want %q", tt.build, tt.terraformVersion, got, tt.want)
		}
	}
}

// TestProviderSendsUserAgent checks that the requests of a configured
// provider carry its User-Agent, including the CLI version.
func TestProviderSendsUserAgent(t *testing.T) {
	t.Parallel()
	fake := newFakePDNS(t)
	var mu sync.Mutex
	userAgents := map[string]bool{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		userAgents[r.Header.Get("User-Agent")] = true
		mu.Unlock()
		fake.serveHTTP(w, r)
	}))
	t.Cleanup(srv.Close)

	tfresource.UnitTest(t, tfresource.TestCase{
		ProtoV6ProviderFactories: testProviderFactories,
		Steps: []tfresource.TestStep{{
			Config: serverIDConfig(srv.URL, "localhost"),
			Check:  checkFakeKey(fake, "k", "hmac-sha256", "YWFhYQ=="),
		}},
	})

	mu.Lock()
	defer mu.Unlock()
	want := regexp.MustCompile(`^terraform-provider-pdns/test \(commit 0123456789ab\) terraform/\d+\.\d+\.\d+`)
	if len(userAgents) != 1 {
		t.Fatalf("User-Agents = %v, want one", userAgents)
	}
	for ua := range userAgents {
		if !want.MatchString(ua) {
			t.Errorf("User-Agent = %q, want %s", ua, want)
		}
	}
}
