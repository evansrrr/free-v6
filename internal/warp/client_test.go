package warp

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestRegisterUsesRegistrationAndEnrollRequests(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/reg":
			var payload map[string]string
			if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
				t.Fatal(err)
			}
			if payload["tunnel_type"] != "wireguard" || payload["key_type"] != "curve25519" {
				t.Fatalf("unexpected registration payload: %#v", payload)
			}
			_, _ = w.Write([]byte(`{"id":"device-1","token":"token-1"}`))
		case r.Method == http.MethodPatch && r.URL.Path == "/reg/device-1":
			if r.Header.Get("Authorization") != "Bearer token-1" {
				t.Fatalf("missing bearer token")
			}
			var payload enrollRequest
			if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
				t.Fatal(err)
			}
			if payload.KeyType != "secp256r1" || payload.TunnelType != "masque" || payload.Key == "" {
				t.Fatalf("unexpected enroll payload: %#v", payload)
			}
			_, _ = w.Write([]byte(`{"id":"device-1","token":"token-1","config":{"interface":{"addresses":{"v4":"172.16.0.2","v6":"2606:4700::2"}},"peers":[{"public_key":"-----BEGIN PUBLIC KEY-----\npeer-key\n-----END PUBLIC KEY-----"}]}}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	client := NewClient(server.Client())
	client.BaseURL = server.URL
	device, err := client.Register(context.Background(), "test-device")
	if err != nil {
		t.Fatal(err)
	}
	if device.DeviceID != "device-1" || device.Token != "token-1" || device.PeerPublicKey != "peer-key" {
		t.Fatalf("unexpected device: %#v", device)
	}
	if strings.TrimSpace(device.PrivateKey) == "" {
		t.Fatal("private key should be present")
	}
}
