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
	switch {
	case errors.As(err, &unauthorizedError):
		diags.AddError("Authorization Error", "Not authorized to access pdns api")
	case errors.As(err, &notFoundError):
		diags.AddError("Zone not found", notFoundError.Error())
	default:
		diags.AddError("Client Error", fmt.Sprintf("Unable to do http request to pdns API, got error: %s", err))
	}

	return true
}
