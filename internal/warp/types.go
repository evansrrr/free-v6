package warp

import "time"

type Device struct {
	DeviceID      string    `json:"deviceId"`
	Token         string    `json:"token"`
	PrivateKey    string    `json:"privateKey"`
	PeerPublicKey string    `json:"peerPublicKey"`
	IPv4          string    `json:"ipv4"`
	IPv6          string    `json:"ipv6"`
	RegisteredAt  time.Time `json:"registeredAt"`
}

type registerResponse struct {
	ID     string       `json:"id"`
	Token  string       `json:"token"`
	Config deviceConfig `json:"config"`
}

type deviceConfig struct {
	Interface interfaceConfig `json:"interface"`
	Peers     []peerConfig    `json:"peers"`
}

type interfaceConfig struct {
	Addresses addressConfig `json:"addresses"`
}

type addressConfig struct {
	V4 string `json:"v4"`
	V6 string `json:"v6"`
}

type peerConfig struct {
	PublicKey string `json:"public_key"`
}

type enrollRequest struct {
	Key        string `json:"key"`
	KeyType    string `json:"key_type"`
	TunnelType string `json:"tunnel_type"`
	Name       string `json:"name,omitempty"`
}
