package mihomo

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestWaitReady(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := WaitReady(ctx, listener.Addr().String(), 10*time.Millisecond); err != nil {
		t.Fatal(err)
	}
}

func TestWaitReadyTimeout(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if err := WaitReady(ctx, "127.0.0.1:1", 5*time.Millisecond); err == nil {
		t.Fatal("expected controller readiness timeout")
	}
}

func TestCheckController(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writer.Header().Set("Content-Type", "application/json")
		switch request.URL.Path {
		case "/version":
			_, _ = writer.Write([]byte(`{"version":"alpha-test"}`))
		case "/proxies":
			_, _ = writer.Write([]byte(`{"proxies":{"🚀 节点选择":{"type":"Selector","now":"♻️ 自动选择"}}}`))
		default:
			http.NotFound(writer, request)
		}
	}))
	defer server.Close()

	status, err := CheckController(context.Background(), strings.TrimPrefix(server.URL, "http://"))
	if err != nil {
		t.Fatal(err)
	}
	if status.Version != "alpha-test" || status.Proxies[NodeSelectionGroup].Now != "♻️ 自动选择" {
		t.Fatalf("unexpected controller status: %+v", status)
	}
}

func TestCheckControllerRequiresNodeSelectionGroup(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writer.Header().Set("Content-Type", "application/json")
		if request.URL.Path == "/version" {
			_, _ = writer.Write([]byte(`{"version":"alpha-test"}`))
			return
		}
		_, _ = writer.Write([]byte(`{"proxies":{}}`))
	}))
	defer server.Close()

	_, err := CheckController(context.Background(), server.URL)
	if err == nil || !strings.Contains(err.Error(), "proxy group") {
		t.Fatalf("expected missing proxy group error, got %v", err)
	}
}

func TestProbeIPv6Egress(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writer.Header().Set("Content-Type", "text/plain")
		_, _ = writer.Write([]byte("2001:db8::42\n"))
	}))
	defer server.Close()

	status, err := ProbeIPv6Egress(context.Background(), server.URL, "")
	if err != nil {
		t.Fatal(err)
	}
	if status.Address != "2001:db8::42" {
		t.Fatalf("unexpected IPv6 address %q", status.Address)
	}
}

func TestProbeIPv6EgressRejectsIPv4(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		_, _ = writer.Write([]byte("192.0.2.42\n"))
	}))
	defer server.Close()

	if _, err := ProbeIPv6Egress(context.Background(), server.URL, ""); err == nil {
		t.Fatal("expected IPv4 egress to be rejected")
	}
}
