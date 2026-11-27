package provider

import (
	"errors"
	"fmt"
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
