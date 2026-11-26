package provider

import (
	"net/netip"
	"slices"
	"strings"
)

// canonicalRecordContent returns the form PowerDNS stores record content of
// the given type in, as far as the provider can tell: it is used to decide
// whether a configured value and the value read back from the server are the
// same record.
//
// PowerDNS stores AAAA addresses in their RFC 5952 form (lowercase, leading
// zeros dropped, the longest zero run compressed) and collapses runs of
// whitespace between the fields of a record (e.g. MX "10   mail." becomes
// "10 mail."). Whitespace inside TXT and SPF content is significant, so it is
// left alone. Everything else is compared verbatim.
func canonicalRecordContent(typ, content string) string {
	switch strings.ToUpper(typ) {
	case "A", "AAAA":
		if addr, err := netip.ParseAddr(content); err == nil {
			return addr.String()
		}
		return content
	case "TXT", "SPF":
		return content
	default:
		return strings.Join(strings.Fields(content), " ")
	}
}

// reconcileRecords returns the records to store in state after a read. prior
// holds the records currently in state (as configured), server the records
// PowerDNS returned.
//
// PowerDNS returns the records of an rrset in its own order and in canonical
// form, so storing them verbatim would produce a diff on every plan. When the
// server holds the same records as prior (compared in canonical form and
// ignoring order), prior is returned unchanged. Otherwise the records were
// changed out of band and the server's records are returned, each one spelled
// as in prior where it matches one, so that the plan only shows the records
// that really changed.
func reconcileRecords(typ string, prior, server []string) []string {
	canonical := func(values []string) []string {
		out := make([]string, len(values))
		for i, v := range values {
			out[i] = canonicalRecordContent(typ, v)
		}
		return out
	}

	priorCanonical := canonical(prior)
	serverCanonical := canonical(server)

	sortedPrior, sortedServer := slices.Sorted(slices.Values(priorCanonical)), slices.Sorted(slices.Values(serverCanonical))
	if slices.Equal(sortedPrior, sortedServer) {
		return prior
	}

	used := make([]bool, len(prior))
	out := make([]string, len(server))
	for i, s := range server {
		out[i] = s
		for j, p := range priorCanonical {
			if !used[j] && p == serverCanonical[i] {
				used[j] = true
				out[i] = prior[j]
				break
			}
		}
	}
	return out
}
