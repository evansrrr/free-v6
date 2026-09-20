package mihomo

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	netURL "net/url"
	"strings"
	"time"
)

const NodeSelectionGroup = "🚀 节点选择"

type ControllerStatus struct {
	Version string
	Proxies map[string]ProxyStatus
}

type ProxyStatus struct {
	Type string `json:"type"`
	Now  string `json:"now"`
}

type EgressStatus struct {
	Address string
}

func WaitReady(ctx context.Context, address string, interval time.Duration) error {
	if address == "" {
		return fmt.Errorf("mihomo controller address is empty")
	}
	if interval <= 0 {
		interval = 100 * time.Millisecond
	}
	for {
		connection, err := net.DialTimeout("tcp", address, interval)
		if err == nil {
			_ = connection.Close()
			return nil
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("wait for mihomo controller %s: %w", address, ctx.Err())
		case <-time.After(interval):
		}
	}
}

func CheckController(ctx context.Context, address string) (ControllerStatus, error) {
	var status ControllerStatus
	if address == "" {
		return status, fmt.Errorf("mihomo controller address is empty")
	}
	baseURL := address
	if !strings.HasPrefix(baseURL, "http://") && !strings.HasPrefix(baseURL, "https://") {
		baseURL = "http://" + baseURL
	}
	client := &http.Client{Timeout: 3 * time.Second}
	if err := getControllerJSON(ctx, client, baseURL+"/version", &status); err != nil {
		return ControllerStatus{}, fmt.Errorf("read mihomo version: %w", err)
	}
	var proxyResponse struct {
		Proxies map[string]ProxyStatus `json:"proxies"`
	}
	if err := getControllerJSON(ctx, client, baseURL+"/proxies", &proxyResponse); err != nil {
		return ControllerStatus{}, fmt.Errorf("read mihomo proxies: %w", err)
	}
	status.Proxies = proxyResponse.Proxies
	if _, ok := status.Proxies[NodeSelectionGroup]; !ok {
		return ControllerStatus{}, fmt.Errorf("mihomo proxy group %q is missing", NodeSelectionGroup)
	}
	return status, nil
}

func WaitHealthy(ctx context.Context, address string, interval time.Duration) (ControllerStatus, error) {
	if interval <= 0 {
		interval = 100 * time.Millisecond
	}
	var lastErr error
	for {
		status, err := CheckController(ctx, address)
		if err == nil {
			return status, nil
		}
		lastErr = err
		select {
		case <-ctx.Done():
			return ControllerStatus{}, fmt.Errorf("mihomo controller health failed: %w (last error: %v)", ctx.Err(), lastErr)
		case <-time.After(interval):
		}
	}
}

func ProbeIPv6Egress(ctx context.Context, url string, proxyAddr string) (EgressStatus, error) {
	if url == "" {
		return EgressStatus{}, fmt.Errorf("IPv6 egress URL is empty")
	}
	transport := &http.Transport{}
	if proxyAddr != "" {
		proxyURL, err := netURL.Parse("http://" + proxyAddr)
		if err != nil {
			return EgressStatus{}, fmt.Errorf("parse proxy address %q: %w", proxyAddr, err)
		}
		transport.Proxy = http.ProxyURL(proxyURL)
	}
	client := &http.Client{Timeout: 5 * time.Second, Transport: transport}
	var lastErr error
	for attempt := 0; attempt < 12; attempt++ {
		if attempt > 0 {
			select {
			case <-ctx.Done():
				return EgressStatus{}, fmt.Errorf("IPv6 egress probe timed out: %w (last: %v)", ctx.Err(), lastErr)
			case <-time.After(3 * time.Second):
			}
		}
		status, err := probeIPv6EgressOnce(ctx, client, url)
		if err == nil {
			return status, nil
		}
		lastErr = err
	}
	return EgressStatus{}, fmt.Errorf("IPv6 egress probe failed after retries: %w", lastErr)
}

func probeIPv6EgressOnce(ctx context.Context, client *http.Client, url string) (EgressStatus, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return EgressStatus{}, err
	}
	response, err := client.Do(request)
	if err != nil {
		return EgressStatus{}, fmt.Errorf("request IPv6 egress URL: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		return EgressStatus{}, fmt.Errorf("IPv6 egress URL returned HTTP %s", response.Status)
	}
	var body struct {
		Address string `json:"ip"`
	}
	if strings.Contains(response.Header.Get("Content-Type"), "application/json") {
		if err := json.NewDecoder(response.Body).Decode(&body); err != nil {
			return EgressStatus{}, fmt.Errorf("decode IPv6 egress response: %w", err)
		}
	} else {
		data, err := io.ReadAll(io.LimitReader(response.Body, 256))
		if err != nil {
			return EgressStatus{}, fmt.Errorf("read IPv6 egress response: %w", err)
		}
		body.Address = strings.TrimSpace(string(data))
	}
	address := net.ParseIP(strings.TrimSpace(body.Address))
	if address == nil || address.To4() != nil {
		return EgressStatus{}, fmt.Errorf("egress response is not an IPv6 address: %q", body.Address)
	}
	return EgressStatus{Address: address.String()}, nil
}

func getControllerJSON(ctx context.Context, client *http.Client, url string, target any) error {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	response, err := client.Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		return fmt.Errorf("HTTP %s", response.Status)
	}
	if err := json.NewDecoder(response.Body).Decode(target); err != nil {
		return err
	}
	return nil
}
