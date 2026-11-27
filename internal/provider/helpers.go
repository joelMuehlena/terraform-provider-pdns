package provider

import (
	"context"
	"errors"
	"fmt"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"strings"

	"github.com/hashicorp/terraform-plugin-framework/diag"
	"gitlab.com/joelMuehlena/homelab/code/terraform/provider/terraform-provider-pdns/internal/pdns_client"
)

// fqdn returns name unchanged when it is already fully qualified (ends with a
// dot); otherwise it suffixes it with the zone name.
func fqdn(name, zone string) string {
	if strings.HasSuffix(name, ".") {
		return name
	}
	return name + "." + zone
}

// handleClientError translates a pdns_client error into diagnostics. It returns
// true when err is non-nil (and a diagnostic was added), so callers can early
// return with `if handleClientError(&resp.Diagnostics, err) { return }`.
func handleClientError(diags *diag.Diagnostics, err error) bool {
	if err == nil {
		return false
	}

	var unauthorizedError *pdns_client.PDNSUnauthorizedError
	var notFoundError *pdns_client.PDNSZoneNotFoundError
	var keyNotFoundError *pdns_client.PDNSTSIGKeyNotFoundError
	switch {
	case errors.As(err, &unauthorizedError):
		diags.AddError("Authorization Error", "Not authorized to access pdns api")
	case errors.As(err, &notFoundError):
		diags.AddError("Zone not found", notFoundError.Error())
	case errors.As(err, &keyNotFoundError):
		diags.AddError("TSIG key not found", keyNotFoundError.Error())
	default:
		diags.AddError("Client Error", fmt.Sprintf("Unable to do http request to pdns API, got error: %s", err))
	}

	return true
}

// isNotFound reports whether err signals that the requested zone or TSIG key
// does not exist on the server.
func isNotFound(err error) bool {
	var zoneNotFound *pdns_client.PDNSZoneNotFoundError
	var keyNotFound *pdns_client.PDNSTSIGKeyNotFoundError
	return errors.As(err, &zoneNotFound) || errors.As(err, &keyNotFound)
}

// sameDNSName compares two DNS names, ignoring a trailing dot. Like DNS
// (RFC 4343) only ASCII letters compare case-insensitively.
func sameDNSName(a, b string) bool {
	return asciiEqualFold(strings.TrimSuffix(a, "."), strings.TrimSuffix(b, "."))
}

// asciiEqualFold reports whether a and b are equal, comparing ASCII letters
// case-insensitively and every other byte exactly. Unlike strings.EqualFold
// it applies no Unicode case folding, which DNS does not know (e.g. U+212A
// KELVIN SIGN is not "k").
func asciiEqualFold(a, b string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := 0; i < len(a); i++ {
		if asciiLower(a[i]) != asciiLower(b[i]) {
			return false
		}
	}
	return true
}

func asciiLower(c byte) byte {
	if 'A' <= c && c <= 'Z' {
		return c + 'a' - 'A'
	}
	return c
}

// dnsNameValidator validates a name that the provider qualifies with fqdn:
// either relative to the zone ("ns1", "a.b") or fully qualified with a
// trailing dot ("ns1.example.com."). It rejects what fqdn would turn into a
// broken name: an empty name, "@" (the provider has no relative spelling for
// the zone apex, use its fully qualified name), a leading dot, empty labels
// and whitespace.
type dnsNameValidator struct{}

func (v dnsNameValidator) Description(context.Context) string {
	return "must be a DNS name relative to the zone or fully qualified with a trailing dot; use the fully qualified name for the zone apex"
}

func (v dnsNameValidator) MarkdownDescription(ctx context.Context) string {
	return v.Description(ctx)
}

func (v dnsNameValidator) ValidateString(_ context.Context, req validator.StringRequest, resp *validator.StringResponse) {
	if req.ConfigValue.IsNull() || req.ConfigValue.IsUnknown() {
		return
	}
	name := req.ConfigValue.ValueString()

	var problem string
	switch {
	case name == "":
		problem = "the name is empty"
	case name == "@":
		problem = `"@" is not supported; use the fully qualified name of the zone apex, e.g. "example.com."`
	case strings.HasPrefix(name, "."):
		problem = "the name starts with a dot"
	case strings.Contains(name, ".."):
		problem = "the name contains an empty label (\"..\")"
	case strings.ContainsAny(name, " \t\r\n"):
		problem = "the name contains whitespace"
	default:
		return
	}
	resp.Diagnostics.AddAttributeError(req.Path, "Invalid DNS name",
		fmt.Sprintf("%q is not a valid name: %s. Give it relative to the zone (\"ns1\") or fully qualified with a trailing dot (\"ns1.example.com.\").", name, problem))
}

// rnameValidator rejects an rname written as email address
// ("hostmaster@example.com"): the SOA record holds the mailbox as DNS name,
// with the "@" replaced by a dot ("hostmaster.example.com."). A lone "@" is
// left to dnsNameValidator.
type rnameValidator struct{}

func (v rnameValidator) Description(context.Context) string {
	return `must be the mailbox as DNS name ("hostmaster.example.com."), not an email address with "@"`
}

func (v rnameValidator) MarkdownDescription(ctx context.Context) string {
	return v.Description(ctx)
}

func (v rnameValidator) ValidateString(_ context.Context, req validator.StringRequest, resp *validator.StringResponse) {
	if req.ConfigValue.IsNull() || req.ConfigValue.IsUnknown() {
		return
	}
	rname := req.ConfigValue.ValueString()
	at := strings.LastIndex(rname, "@")
	if rname == "@" || at < 0 {
		return
	}

	local, domain := rname[:at], strings.TrimSuffix(rname[at+1:], ".")
	hint := `"user.example.com."`
	if local != "" && domain != "" && !strings.Contains(local, "@") {
		hint = fmt.Sprintf("%q", strings.ReplaceAll(local, ".", `\.`)+"."+domain+".")
	}
	resp.Diagnostics.AddAttributeError(req.Path, "Invalid rname",
		fmt.Sprintf("%q contains \"@\": the rname is the administrator's mailbox written as DNS name, with the \"@\" replaced by a dot "+
			"(and dots before it escaped as \"\\.\"). Write %s instead.", rname, hint))
}
