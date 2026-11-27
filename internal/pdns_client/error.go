package pdns_client

import "fmt"

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

// PDNSServerNotFoundError is returned when the configured server does not
// exist at the endpoint (or the endpoint is not the base URL of the API).
type PDNSServerNotFoundError struct {
	ServerID string
	Endpoint string
}

func (e *PDNSServerNotFoundError) Error() string {
	return "server_id \"" + e.ServerID + "\" not found at endpoint " + e.Endpoint +
		" (GET /api/v1/servers/" + e.ServerID + " returned 404); check the provider's endpoint and server_id"
}

// statusError is a response with an unexpected status code that has no
// dedicated error type. Error() includes the response body.
type statusError struct {
	StatusCode int
	Body       string
}

func (e *statusError) Error() string {
	return fmt.Sprintf("unexpected status code %d: %s", e.StatusCode, e.Body)
}
