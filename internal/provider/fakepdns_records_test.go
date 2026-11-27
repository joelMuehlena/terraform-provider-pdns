package provider

import (
	"net/netip"
	"slices"
	"strings"

	"gitlab.com/joelMuehlena/homelab/code/terraform/provider/terraform-provider-pdns/internal/pdns_client"
)

// fakeStoreRrset turns an rrset as sent in a PATCH into the form PowerDNS
// stores (and later returns) it in, as observed on pdns-auth 5.1:
//   - the owner name is lowercased ("WWW.example.com." is stored as
//     "www.example.com."), the contents keep their case,
//   - the type is uppercased ("a" is stored as "A"),
//   - the content of A and AAAA records is canonicalized
//     ("2345:0425:2CA1:0000:0000:0567:5673:23b5" becomes "2345:425:2ca1::567:5673:23b5"),
//   - runs of whitespace between the fields of other records collapse
//     ("10   mail." becomes "10 mail."), for TXT and SPF only outside the
//     quoted strings ("a"   "b" becomes "a" "b"),
//   - the records are sorted by content, whatever order they were sent in.
func fakeStoreRrset(rr pdns_client.Rrset) pdns_client.Rrset {
	rr.Name = strings.ToLower(rr.Name)
	rr.Type = strings.ToUpper(rr.Type)
	records := slices.Clone(rr.Records)
	for i := range records {
		switch rr.Type {
		case "A", "AAAA":
			if addr, err := netip.ParseAddr(records[i].Content); err == nil {
				records[i].Content = addr.String()
			}
		case "TXT", "SPF":
			records[i].Content = canonicalCharacterStrings(records[i].Content)
		default:
			records[i].Content = strings.Join(strings.Fields(records[i].Content), " ")
		}
	}
	slices.SortFunc(records, func(a, b pdns_client.Record) int { return strings.Compare(a.Content, b.Content) })
	rr.Records = records
	return rr
}
