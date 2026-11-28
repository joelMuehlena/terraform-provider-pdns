package pdns_client

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/hashicorp/terraform-plugin-log/tflogtest"
)

// serverLogs runs two requests with the server check against a server whose
// server object is body and returns the decoded log entries and the raw log.
func serverLogs(t *testing.T, endpointUserinfo, body string) ([]map[string]any, string) {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/v1/servers/localhost" {
			_, _ = w.Write([]byte(body))
			return
		}
		_, _ = w.Write([]byte(`{}`))
	}))
	t.Cleanup(srv.Close)
	endpoint := strings.Replace(srv.URL, "http://", "http://"+endpointUserinfo, 1)
	client := NewPDNSClient(srv.Client(), endpoint, "localhost", testAPIKey, WithServerCheck())

	var out bytes.Buffer
	ctx := tflogtest.RootLogger(context.Background(), &out)
	for range 2 {
		if _, err := client.GetZone(ctx, "z.", false, ""); err != nil {
			t.Fatal(err)
		}
	}
	raw := out.String()
	entries, err := tflogtest.MultilineJSONDecode(&out)
	if err != nil {
		t.Fatal(err)
	}
	return entries, raw
}

func logsWithLevel(entries []map[string]any, level string) []map[string]any {
	var found []map[string]any
	for _, e := range entries {
		if e["@level"] == level {
			found = append(found, e)
		}
	}
	return found
}

func TestServerCheckLogsServer(t *testing.T) {
	t.Parallel()
	entries, raw := serverLogs(t, "user:hunter2@", `{"id":"localhost","type":"Server","daemon_type":"authoritative","version":"5.1.2","url":"/api/v1/servers/localhost"}`)

	infos := logsWithLevel(entries, "info")
	if len(infos) != 1 {
		t.Fatalf("info entries = %v, want one", infos)
	}
	e := infos[0]
	for k, v := range map[string]string{"@message": "Connected to PowerDNS", "server_id": "localhost", "daemon_type": "authoritative", "version": "5.1.2"} {
		if e[k] != v {
			t.Errorf("%s = %#v, want %q", k, e[k], v)
		}
	}
	if ep, _ := e["endpoint"].(string); !strings.HasPrefix(ep, "http://user:xxxxx@127.0.0.1:") {
		t.Errorf("endpoint = %q, want the endpoint with the password redacted", ep)
	}
	if w := logsWithLevel(entries, "warn"); len(w) != 0 {
		t.Errorf("unexpected warnings: %v", w)
	}
	for _, secret := range []string{testAPIKey, "hunter2"} {
		if strings.Contains(raw, secret) {
			t.Errorf("the logs contain %q", secret)
		}
	}
}

func TestServerCheckWarnsAboutUntestedVersion(t *testing.T) {
	t.Parallel()
	for body, wantWarning := range map[string]bool{
		`{"daemon_type":"authoritative","version":"4.8.4"}`:      true,
		`{"daemon_type":"authoritative","version":"5.2.0"}`:      true,
		`{"daemon_type":"authoritative","version":"4.9.10"}`:     false,
		`{"daemon_type":"authoritative","version":"0.0.0.gabc"}`: false,
		`{"daemon_type":"recursor","version":"4.8.0"}`:           false,
		`not json`: false,
	} {
		entries, _ := serverLogs(t, "", body)
		warnings := logsWithLevel(entries, "warn")
		if got := len(warnings) == 1; got != wantWarning || len(warnings) > 1 {
			t.Errorf("%s: warnings = %v, want a warning: %v", body, warnings, wantWarning)
		}
		if len(logsWithLevel(entries, "info")) != 1 {
			t.Errorf("%s: no single info entry in %v", body, entries)
		}
	}
}

func TestTestedVersion(t *testing.T) {
	t.Parallel()
	for v, want := range map[string]bool{
		"4.9.0": true, "4.9.10": true, "5.0.1": true, "5.1.2": true, "5.1.0-alpha1": true, "5.1": true, "4.10.0": true,
		"4.8.4": false, "5.2.0": false, "6.0.0": false, "3.4.11": false,
		"": true, "0.0.1234.gabcdef": true, "master": true, "5": true, "x5.0.0": true,
	} {
		if got := testedVersion(v); got != want {
			t.Errorf("testedVersion(%q) = %v, want %v", v, got, want)
		}
	}
}
