package pdns_client

import (
	"context"
	"errors"
	"net/http"
	"testing"
)

func TestCreateTSIGKey(t *testing.T) {
	t.Parallel()
	client, requests := newTestClient(t, http.StatusCreated, `{"id":"tenant-a.","name":"tenant-a","algorithm":"hmac-sha256","key":"YWFhYQ==","type":"TSIGKey"}`)

	key, err := client.CreateTSIGKey(context.Background(), TSIGKey{Name: "tenant-a", Algorithm: "hmac-sha256", Key: "YWFhYQ=="})
	if err != nil {
		t.Fatal(err)
	}

	req := onlyRequest(t, requests)
	if req.Method != http.MethodPost || req.Path != "/api/v1/servers/localhost/tsigkeys" {
		t.Errorf("request = %s %s", req.Method, req.Path)
	}
	assertJSON(t, req.Body, `{"name":"tenant-a","algorithm":"hmac-sha256","key":"YWFhYQ=="}`)
	if key.ID != "tenant-a." || key.Key != "YWFhYQ==" {
		t.Errorf("unexpected key: %+v", key)
	}
}

func TestGetTSIGKey(t *testing.T) {
	t.Parallel()
	client, requests := newTestClient(t, http.StatusOK, `{"id":"tenant-a.","name":"tenant-a","algorithm":"hmac-sha512","key":"YWFhYQ=="}`)

	key, err := client.GetTSIGKey(context.Background(), "tenant-a.")
	if err != nil {
		t.Fatal(err)
	}

	req := onlyRequest(t, requests)
	if req.Method != http.MethodGet || req.Path != "/api/v1/servers/localhost/tsigkeys/tenant-a." {
		t.Errorf("request = %s %s", req.Method, req.Path)
	}
	if key.Name != "tenant-a" || key.Algorithm != "hmac-sha512" {
		t.Errorf("unexpected key: %+v", key)
	}
}

func TestUpdateTSIGKeyOnlySendsSetFields(t *testing.T) {
	t.Parallel()
	client, requests := newTestClient(t, http.StatusOK, `{"id":"tenant-a."}`)

	if _, err := client.UpdateTSIGKey(context.Background(), "tenant-a.", TSIGKey{Key: "YmJiYg=="}); err != nil {
		t.Fatal(err)
	}

	req := onlyRequest(t, requests)
	if req.Method != http.MethodPut || req.Path != "/api/v1/servers/localhost/tsigkeys/tenant-a." {
		t.Errorf("request = %s %s", req.Method, req.Path)
	}
	assertJSON(t, req.Body, `{"key":"YmJiYg=="}`)
}

func TestDeleteTSIGKey(t *testing.T) {
	t.Parallel()
	client, requests := newTestClient(t, http.StatusNoContent, "")

	if err := client.DeleteTSIGKey(context.Background(), "tenant-a."); err != nil {
		t.Fatal(err)
	}

	req := onlyRequest(t, requests)
	if req.Method != http.MethodDelete || req.Path != "/api/v1/servers/localhost/tsigkeys/tenant-a." {
		t.Errorf("request = %s %s", req.Method, req.Path)
	}
}

func TestTSIGKeyNotFound(t *testing.T) {
	t.Parallel()
	client, _ := newTestClient(t, http.StatusNotFound, "")

	err := client.DeleteTSIGKey(context.Background(), "gone.")
	var e *PDNSTSIGKeyNotFoundError
	if !errors.As(err, &e) || e.KeyID != "gone." {
		t.Fatalf("expected PDNSTSIGKeyNotFoundError for gone., got %v", err)
	}
	if e.Error() == "" {
		t.Error("empty error message")
	}
}

func TestTSIGKeyIDIsEscaped(t *testing.T) {
	t.Parallel()
	client, requests := newTestClient(t, http.StatusOK, `{}`)

	if _, err := client.GetTSIGKey(context.Background(), "a/b."); err != nil {
		t.Fatal(err)
	}
	if got := onlyRequest(t, requests).Path; got != "/api/v1/servers/localhost/tsigkeys/a%2Fb." {
		t.Errorf("path = %q", got)
	}
}

func TestZoneMetadata(t *testing.T) {
	t.Parallel()

	t.Run("get", func(t *testing.T) {
		t.Parallel()
		client, requests := newTestClient(t, http.StatusOK, `{"kind":"TSIG-ALLOW-DNSUPDATE","metadata":["a","b"]}`)

		values, err := client.GetZoneMetadata(context.Background(), "example.com.", "TSIG-ALLOW-DNSUPDATE")
		if err != nil {
			t.Fatal(err)
		}
		req := onlyRequest(t, requests)
		if req.Method != http.MethodGet || req.Path != "/api/v1/servers/localhost/zones/example.com./metadata/TSIG-ALLOW-DNSUPDATE" {
			t.Errorf("request = %s %s", req.Method, req.Path)
		}
		if len(values) != 2 || values[0] != "a" || values[1] != "b" {
			t.Errorf("values = %v", values)
		}
	})

	t.Run("set", func(t *testing.T) {
		t.Parallel()
		client, requests := newTestClient(t, http.StatusOK, "")

		if err := client.SetZoneMetadata(context.Background(), "example.com.", "TSIG-ALLOW-DNSUPDATE", []string{"a"}); err != nil {
			t.Fatal(err)
		}
		req := onlyRequest(t, requests)
		if req.Method != http.MethodPut || req.Path != "/api/v1/servers/localhost/zones/example.com./metadata/TSIG-ALLOW-DNSUPDATE" {
			t.Errorf("request = %s %s", req.Method, req.Path)
		}
		assertJSON(t, req.Body, `{"kind":"TSIG-ALLOW-DNSUPDATE","metadata":["a"]}`)
	})

	t.Run("set nil sends empty list", func(t *testing.T) {
		t.Parallel()
		client, requests := newTestClient(t, http.StatusOK, "")

		if err := client.SetZoneMetadata(context.Background(), "example.com.", "TSIG-ALLOW-DNSUPDATE", nil); err != nil {
			t.Fatal(err)
		}
		assertJSON(t, onlyRequest(t, requests).Body, `{"kind":"TSIG-ALLOW-DNSUPDATE","metadata":[]}`)
	})

	t.Run("zone not found", func(t *testing.T) {
		t.Parallel()
		client, _ := newTestClient(t, http.StatusNotFound, "")

		_, err := client.GetZoneMetadata(context.Background(), "missing.", "TSIG-ALLOW-DNSUPDATE")
		if _, ok := errors.AsType[*PDNSZoneNotFoundError](err); !ok {
			t.Fatalf("expected PDNSZoneNotFoundError, got %v", err)
		}
	})
}

func TestErrorMessages(t *testing.T) {
	t.Parallel()
	for _, err := range []error{&PDNSZoneNotFoundError{ZoneID: "z."}, &PDNSUnauthorizedError{}, &PDNSTSIGKeyNotFoundError{KeyID: "k."}} {
		if err.Error() == "" {
			t.Errorf("%T has an empty message", err)
		}
	}
}

func TestDecodeErrors(t *testing.T) {
	t.Parallel()

	calls := map[string]func(*PDNSClient) error{
		"get tsig key": func(c *PDNSClient) error {
			_, err := c.GetTSIGKey(context.Background(), "k.")
			return err
		},
		"create tsig key": func(c *PDNSClient) error {
			_, err := c.UpdateTSIGKey(context.Background(), "k.", TSIGKey{Key: "YQ=="})
			return err
		},
		"get metadata": func(c *PDNSClient) error {
			_, err := c.GetZoneMetadata(context.Background(), "z.", "TSIG-ALLOW-DNSUPDATE")
			return err
		},
	}

	for name, call := range calls {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			client, _ := newTestClient(t, http.StatusOK, "not json")
			if err := call(client); err == nil {
				t.Fatal("expected decode error")
			}
		})
	}
}

func TestTransportError(t *testing.T) {
	t.Parallel()
	// Nothing listens on this address, so every request fails before a response.
	client := NewPDNSClient(http.DefaultClient, "http://127.0.0.1:1", "localhost", testAPIKey)

	calls := map[string]func() error{
		"get zone":       func() error { _, err := client.GetZone(context.Background(), "z.", false, ""); return err },
		"create zone":    func() error { _, err := client.CreateZone(context.Background(), PDNSZone{Name: "z."}); return err },
		"update zone":    func() error { return client.UpdateZone(context.Background(), "z.", PDNSZone{Kind: "Native"}) },
		"update records": func() error { return client.UpdateZoneRecords(context.Background(), "z.", nil) },
		"delete zone":    func() error { return client.DeleteZone(context.Background(), "z.") },
		"create key":     func() error { _, err := client.CreateTSIGKey(context.Background(), TSIGKey{Name: "k"}); return err },
		"delete key":     func() error { return client.DeleteTSIGKey(context.Background(), "k.") },
		"set metadata":   func() error { return client.SetZoneMetadata(context.Background(), "z.", "X", nil) },
		"set key ids":    func() error { return client.UpdateZoneTSIGKeyIDs(context.Background(), "z.", nil, nil) },
	}

	for name, call := range calls {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			if err := call(); err == nil {
				t.Fatal("expected transport error")
			}
		})
	}
}

func TestInvalidEndpoint(t *testing.T) {
	t.Parallel()
	client := NewPDNSClient(http.DefaultClient, "http://bad host", "localhost", testAPIKey)

	if _, err := client.GetZone(context.Background(), "z.", false, ""); err == nil {
		t.Fatal("expected request construction error")
	}
}
