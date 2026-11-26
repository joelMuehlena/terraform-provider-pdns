package pdns_client

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/url"
)

// TSIGKey is a TSIG key as represented by the PowerDNS API.
type TSIGKey struct {
	ID        string `json:"id,omitempty"`
	Name      string `json:"name,omitempty"`
	Algorithm string `json:"algorithm,omitempty"`
	Key       string `json:"key,omitempty"`
	Type      string `json:"type,omitempty"`
}

func tsigKeyPath(keyID string) string {
	return "tsigkeys/" + url.PathEscape(keyID)
}

func (client *PDNSClient) CreateTSIGKey(ctx context.Context, key TSIGKey) (TSIGKey, error) {
	return client.sendTSIGKey(ctx, http.MethodPost, "tsigkeys", key.Name, key, http.StatusCreated)
}

func (client *PDNSClient) GetTSIGKey(ctx context.Context, keyID string) (TSIGKey, error) {
	resp, err := client.do(ctx, http.MethodGet, tsigKeyPath(keyID), &PDNSTSIGKeyNotFoundError{KeyID: keyID}, nil, http.StatusOK)
	if err != nil {
		return TSIGKey{}, err
	}
	defer func() { _ = resp.Body.Close() }()

	var key TSIGKey
	if err := json.NewDecoder(resp.Body).Decode(&key); err != nil {
		return TSIGKey{}, err
	}
	return key, nil
}

// UpdateTSIGKey changes the fields set in key (name, algorithm, key) of the
// TSIG key identified by keyID. Empty fields are left untouched.
//
// Note: changing the algorithm is not reliable; the gsqlite3/gmysql/gpgsql
// backends key rows on (name, algorithm) and insert a second row instead.
func (client *PDNSClient) UpdateTSIGKey(ctx context.Context, keyID string, key TSIGKey) (TSIGKey, error) {
	return client.sendTSIGKey(ctx, http.MethodPut, tsigKeyPath(keyID), keyID, key, http.StatusOK)
}

func (client *PDNSClient) DeleteTSIGKey(ctx context.Context, keyID string) error {
	resp, err := client.do(ctx, http.MethodDelete, tsigKeyPath(keyID), &PDNSTSIGKeyNotFoundError{KeyID: keyID}, nil, http.StatusNoContent)
	if err != nil {
		return err
	}
	_ = resp.Body.Close()
	return nil
}

func (client *PDNSClient) sendTSIGKey(ctx context.Context, method, apiPath, keyID string, key TSIGKey, wantStatus int) (TSIGKey, error) {
	data, err := json.Marshal(key)
	if err != nil {
		return TSIGKey{}, err
	}

	resp, err := client.do(ctx, method, apiPath, &PDNSTSIGKeyNotFoundError{KeyID: keyID}, bytes.NewReader(data), wantStatus)
	if err != nil {
		return TSIGKey{}, err
	}
	defer func() { _ = resp.Body.Close() }()

	var out TSIGKey
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return TSIGKey{}, err
	}
	return out, nil
}
