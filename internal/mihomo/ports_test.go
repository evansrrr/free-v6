package mihomo

import "testing"

func TestRequiredListenPortsAreDefined(t *testing.T) {
	if len(requiredListenPorts) != 4 {
		t.Fatalf("expected four required listen checks, got %d", len(requiredListenPorts))
	}
}

func TestListenPortSupportsUDP(t *testing.T) {
	listener, err := listenPort(ListenPort{Network: "udp", Address: "127.0.0.1:0"})
	if err != nil {
		t.Fatalf("listenPort returned an error for UDP: %v", err)
	}
	defer listener.Close()
}
