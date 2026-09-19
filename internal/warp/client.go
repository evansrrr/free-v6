package warp

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

const defaultAPI = "https://api.cloudflareclient.com/v0a4471"

var defaultHeaders = map[string]string{
	"User-Agent":        "WARP for Android",
	"CF-Client-Version": "a-6.35-4471",
	"Content-Type":      "application/json; charset=UTF-8",
}

type Client struct {
	BaseURL    string
	HTTPClient *http.Client
}

func NewClient(httpClient *http.Client) *Client {
	if httpClient == nil {
		httpClient = &http.Client{Timeout: 20 * time.Second}
	}
	return &Client{BaseURL: defaultAPI, HTTPClient: httpClient}
}

func (c *Client) Register(ctx context.Context, deviceName string) (Device, error) {
	wgKey, err := randomBase64(32)
	if err != nil {
		return Device{}, fmt.Errorf("generate registration key: %w", err)
	}
	serial, err := randomHex(8)
	if err != nil {
		return Device{}, fmt.Errorf("generate device serial: %w", err)
	}

	registration := map[string]string{
		"key": wgKey, "install_id": "", "fcm_token": "", "tos": cloudflareTime(),
		"model": "PC", "serial_number": serial, "os_version": "",
		"key_type": "curve25519", "tunnel_type": "wireguard", "locale": "en-US",
	}
	var account registerResponse
	if err := c.doJSON(ctx, http.MethodPost, "/reg", "", registration, &account); err != nil {
		return Device{}, fmt.Errorf("register WARP device: %w", err)
	}
	if account.ID == "" || account.Token == "" {
		return Device{}, fmt.Errorf("register WARP device: response missing id or token")
	}

	privateKey, publicKey, err := generateMASQUEKeyPair()
	if err != nil {
		return Device{}, err
	}
	update := enrollRequest{Key: publicKey, KeyType: "secp256r1", TunnelType: "masque", Name: deviceName}
	var enrolled registerResponse
	if err := c.doJSON(ctx, http.MethodPatch, "/reg/"+account.ID, account.Token, update, &enrolled); err != nil {
		return Device{}, fmt.Errorf("enroll MASQUE key: %w", err)
	}
	peerKey := ""
	if len(enrolled.Config.Peers) > 0 {
		peerKey = strings.ReplaceAll(enrolled.Config.Peers[0].PublicKey, "-----BEGIN PUBLIC KEY-----", "")
		peerKey = strings.ReplaceAll(peerKey, "-----END PUBLIC KEY-----", "")
		peerKey = strings.Join(strings.Fields(peerKey), "")
	}
	if peerKey == "" {
		return Device{}, fmt.Errorf("enroll MASQUE key: response missing peer public key")
	}

	return Device{
		DeviceID: account.ID, Token: account.Token, PrivateKey: privateKey, PeerPublicKey: peerKey,
		IPv4: enrolled.Config.Interface.Addresses.V4, IPv6: enrolled.Config.Interface.Addresses.V6,
		RegisteredAt: time.Now().UTC(),
	}, nil
}

func (c *Client) doJSON(ctx context.Context, method, path, token string, input, output any) error {
	body, err := json.Marshal(input)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, method, strings.TrimRight(c.BaseURL, "/")+path, bytes.NewReader(body))
	if err != nil {
		return err
	}
	for key, value := range defaultHeaders {
		req.Header.Set(key, value)
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := c.HTTPClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	responseBody, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(responseBody)))
	}
	if err := json.Unmarshal(responseBody, output); err != nil {
		return fmt.Errorf("decode response: %w", err)
	}
	return nil
}

func randomBase64(size int) (string, error) {
	buf := make([]byte, size)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return base64.StdEncoding.EncodeToString(buf), nil
}

func randomHex(size int) (string, error) {
	buf := make([]byte, size)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return hex.EncodeToString(buf), nil
}

func cloudflareTime() string { return time.Now().UTC().Format("2006-01-02T15:04:05.000-07:00") }
