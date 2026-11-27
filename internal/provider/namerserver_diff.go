package provider

import (
	"net"
	"net/netip"
	"slices"

	"github.com/samber/lo"
)

// Diff compares the planned nameservers a with the current ones b of the
// zone. Nameservers are identified by their fully qualified, lowercased
// hostname, so respelling a hostname ("ns1", "ns1.example.com.", "NS1") is
// no change. It returns the added nameservers and those whose glue addresses
// changed, and the removed ones. Glue addresses compare as IP addresses
// regardless of their spelling, order and attribute, so moving an address
// from address to addresses is no change either.
func Diff(a, b []Nameserver, zone string) ([]Nameserver, []Nameserver) {
	oldState := make(map[string]Nameserver)
	var order []string
	for _, ns := range b {
		key := nameserverKey(ns.Hostname, zone)
		if _, exists := oldState[key]; !exists {
			order = append(order, key)
		}
		oldState[key] = ns
	}

	var addedOrChanged []Nameserver
	var deleted []Nameserver

	// Find added or changed items
	for _, ns := range a {
		key := nameserverKey(ns.Hostname, zone)
		oldNs, exists := oldState[key]
		if !exists || !sameGlue(oldNs.glueAddresses(), ns.glueAddresses()) {
			// If not in old state, or if any value has changed
			addedOrChanged = append(addedOrChanged, ns)
		}
		delete(oldState, key) // remove from oldState map
	}

	// Remaining items in oldState are the deleted ones, in their order.
	for _, key := range order {
		if ns, ok := oldState[key]; ok {
			deleted = append(deleted, ns)
		}
	}

	return addedOrChanged, deleted
}

// nameserverKey identifies a nameserver of the zone by its fully qualified,
// lowercased hostname. DNS names compare ASCII case-insensitively.
func nameserverKey(hostname, zone string) string {
	return asciiLowerString(fqdn(hostname, zone))
}

// asciiLowerString lowercases the ASCII letters of s, like DNS compares
// names (see sameDNSName).
func asciiLowerString(s string) string {
	b := []byte(s)
	for i := range b {
		b[i] = asciiLower(b[i])
	}
	return string(b)
}

// glueTypes are the types of the glue rrsets of a nameserver.
var glueTypes = []string{"A", "AAAA"}

// glueAddresses returns the glue addresses of the nameserver: its address,
// or else its addresses (at most one of them is set).
func (ns Nameserver) glueAddresses() []string {
	if ns.Address != nil {
		return []string{*ns.Address}
	}
	return ns.Addresses
}

// glueRecords splits glue addresses by the type of their rrset: IPv4
// addresses are the contents of the A rrset, IPv6 addresses those of the
// AAAA rrset, each sorted by IP. Types without addresses are not in the map.
// Addresses that are no IP address are returned as invalid.
func glueRecords(addresses []string) (map[string][]string, []string) {
	glue := map[string][]string{}
	var invalid []string
	for _, address := range addresses {
		recordType, ok := addressRecordType(address)
		if !ok {
			invalid = append(invalid, address)
			continue
		}
		glue[recordType] = append(glue[recordType], address)
	}
	for _, contents := range glue {
		slices.SortStableFunc(contents, func(a, b string) int {
			return netip.MustParseAddr(canonicalIP(a)).Compare(netip.MustParseAddr(canonicalIP(b)))
		})
	}
	return glue, invalid
}

// sameGlue reports whether a and b hold the same IP addresses, in any order
// and spelling ("2001:db8::1" and "2001:DB8:0::1" are the same address).
func sameGlue(a, b []string) bool {
	canonical := func(addresses []string) []string {
		c := lo.Map(addresses, func(address string, _ int) string { return canonicalIP(address) })
		slices.Sort(c)
		return slices.Compact(c)
	}
	return slices.Equal(canonical(a), canonical(b))
}

// canonicalIP returns an IP address in its canonical spelling (IPv6
// lowercased and shortest, IPv4-mapped IPv6 as IPv4), so that spellings of
// one address compare equal. Anything else is returned unchanged.
func canonicalIP(address string) string {
	ip := net.ParseIP(address)
	if ip == nil {
		return address
	}
	return ip.String()
}
