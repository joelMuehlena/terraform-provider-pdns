package provider

import (
	"bytes"
	"context"
	"strings"
	"testing"

	fwprovider "github.com/hashicorp/terraform-plugin-framework/provider"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-go/tftypes"
	"github.com/hashicorp/terraform-plugin-log/tflogtest"
)

// configureRequest returns a ConfigureRequest for the provider configuration
// attrs; unset attributes are null.
func configureRequest(t *testing.T, p fwprovider.Provider, attrs map[string]string) fwprovider.ConfigureRequest {
	t.Helper()
	ctx := context.Background()
	sresp := &fwprovider.SchemaResponse{}
	p.Schema(ctx, fwprovider.SchemaRequest{}, sresp)

	typ := sresp.Schema.Type().TerraformType(ctx).(tftypes.Object)
	values := map[string]tftypes.Value{}
	for name, attrType := range typ.AttributeTypes {
		values[name] = tftypes.NewValue(attrType, nil)
		if v, ok := attrs[name]; ok {
			values[name] = tftypes.NewValue(attrType, v)
		}
	}
	return fwprovider.ConfigureRequest{
		TerraformVersion: "1.17.2",
		Config:           tfsdk.Config{Schema: sresp.Schema, Raw: tftypes.NewValue(typ, values)},
	}
}

// logEntries runs fn with a context that captures the provider logs and
// returns the decoded entries.
func logEntries(t *testing.T, fn func(ctx context.Context)) ([]map[string]any, string) {
	t.Helper()
	var out bytes.Buffer
	fn(tflogtest.RootLogger(context.Background(), &out))
	raw := out.String()
	entries, err := tflogtest.MultilineJSONDecode(&out)
	if err != nil {
		t.Fatal(err)
	}
	return entries, raw
}

// findLog returns the first entry with the given message.
func findLog(entries []map[string]any, msg string) map[string]any {
	for _, e := range entries {
		if e["@message"] == msg {
			return e
		}
	}
	return nil
}

func TestProviderLogsBuildOnConfigure(t *testing.T) {
	t.Parallel()
	p := New(testBuild)()
	req := configureRequest(t, p, map[string]string{"endpoint": "http://127.0.0.1:1", "api_key": "secret-key"})

	entries, raw := logEntries(t, func(ctx context.Context) {
		resp := &fwprovider.ConfigureResponse{}
		p.Configure(ctx, req, resp)
		if resp.Diagnostics.HasError() {
			t.Fatal(resp.Diagnostics)
		}
	})

	e := findLog(entries, "Starting PowerDNS provider")
	if e == nil {
		t.Fatalf("no start log entry in %v", entries)
	}
	want := map[string]any{
		"@level":            "info",
		"version":           "test",
		"commit":            testBuild.Commit,
		"commit_date":       "",
		"modified":          false,
		"go_version":        "go1.0",
		"platform":          "plan9/386",
		"build":             "test (commit 0123456789ab, go1.0 plan9/386)",
		"terraform_version": "1.17.2",
	}
	for k, v := range want {
		if e[k] != v {
			t.Errorf("%s = %#v, want %#v", k, e[k], v)
		}
	}
	if strings.Contains(raw, "secret-key") {
		t.Error("the logs contain the API key")
	}
}
