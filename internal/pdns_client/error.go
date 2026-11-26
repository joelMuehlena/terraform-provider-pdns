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

type PDNSTSIGKeyNotFoundError struct {
	KeyID string
}

func (e *PDNSTSIGKeyNotFoundError) Error() string {
	return "This TSIG key was not found: " + e.KeyID
}
