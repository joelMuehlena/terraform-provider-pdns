package provider

func Diff(a, b []Nameserver) ([]Nameserver, []Nameserver) {
	oldState := make(map[string]Nameserver)
	for _, ns := range b {
		oldState[ns.Hostname] = ns
	}

	var addedOrChanged []Nameserver
	var deleted []Nameserver

	// Find added or changed items
	for _, ns := range a {
		oldNs, exists := oldState[ns.Hostname]
		if !exists || oldNs.Address != ns.Address {
			// If not in old state, or if any value has changed
			addedOrChanged = append(addedOrChanged, ns)
		}
		delete(oldState, ns.Hostname) // remove from oldState map
	}

	// Remaining items in oldState are the deleted ones
	for _, ns := range oldState {
		deleted = append(deleted, ns)
	}

	return addedOrChanged, deleted
}
