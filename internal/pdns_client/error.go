package pdns_client

type PDNSZoneNotFoundError struct {
	ZoneID string
}

func (e *PDNSZoneNotFoundError) Error() string {
	return "This zone was not found: " + e.ZoneID
}

type PDNSUnauthorizedError struct{}

func (e *PDNSUnauthorizedError) Error() string {
	return "Not authorized to access PDNS API"
}
