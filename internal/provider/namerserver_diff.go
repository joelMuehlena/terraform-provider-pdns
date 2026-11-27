package provider

// Diff compares the planned nameservers a with the current ones b of the
// zone. Nameservers are identified by their fully qualified, lowercased
// hostname, so respelling a hostname ("ns1", "ns1.example.com.", "NS1") is
// no change. It returns the added nameservers and those whose address
// changed, and the removed ones.
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
		// Compare addresses by value: plan and state are decoded separately,
		// so equal addresses never share a pointer.
		if !exists || !equalAddress(oldNs.Address, ns.Address) {
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

func equalAddress(a, b *string) bool {
	if a == nil || b == nil {
		return a == b
	}
	return *a == *b
}
