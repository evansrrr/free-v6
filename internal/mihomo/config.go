package mihomo

import (
	"fmt"
	"net"
	"strings"

	"github.com/yourname/freev6/internal/warp"
)

var endpoints = []struct {
	Server string
	Port   int
}{
	{"2606:4700:103::1", 443}, {"2606:4700:103::1", 500},
	{"2606:4700:103::1", 1701}, {"2606:4700:103::1", 4500},
	{"2606:4700:103::1", 4443}, {"2606:4700:103::1", 8443},
	{"2606:4700:103::1", 8095},
	{"2606:4700:103::2", 443}, {"2606:4700:103::2", 500},
	{"2606:4700:103::2", 1701}, {"2606:4700:103::2", 4500},
	{"2606:4700:103::2", 4443}, {"2606:4700:103::2", 8443},
	{"2606:4700:103::2", 8095},
	{"2606:4700:104::1", 443}, {"2606:4700:104::1", 500},
	{"2606:4700:104::1", 1701}, {"2606:4700:104::1", 4500},
	{"2606:4700:104::1", 4443}, {"2606:4700:104::1", 8443},
	{"2606:4700:104::1", 8095},
	{"2606:4700:104::2", 443}, {"2606:4700:104::2", 500},
	{"2606:4700:104::2", 1701}, {"2606:4700:104::2", 4500},
	{"2606:4700:104::2", 4443}, {"2606:4700:104::2", 8443},
	{"2606:4700:104::2", 8095},
}

func Render(w warp.Device) (string, error) {
	for name, value := range map[string]string{"private key": w.PrivateKey, "peer public key": w.PeerPublicKey, "IPv4": w.IPv4, "IPv6": w.IPv6} {
		if strings.TrimSpace(value) == "" {
			return "", fmt.Errorf("missing %s", name)
		}
	}
	var b strings.Builder
	b.WriteString("mixed-port: 7890\nmode: rule\nallow-lan: false\n\nproxies:\n")
	var names []string
	for _, endpoint := range endpoints {
		name := endpointName(endpoint.Server, endpoint.Port)
		names = append(names, name)
		server := endpoint.Server
		if net.ParseIP(server).To4() == nil {
			server = `"` + server + `"`
		}
		fmt.Fprintf(&b, "  - name: %s\n    type: masque\n    server: %s\n    port: %d\n    private-key: %s\n    public-key: %s\n    ip: %s\n    ipv6: %s\n    mtu: 1280\n    udp: true\n    remote-dns-resolve: true\n    dns: [1.1.1.1, 2606:4700:4700::1111]\n", name, server, endpoint.Port, w.PrivateKey, w.PeerPublicKey, w.IPv4, w.IPv6)
	}
	b.WriteString("\nproxy-groups:\n  - name: 节点选择\n    type: select\n    proxies:\n")
	for _, name := range names {
		fmt.Fprintf(&b, "      - %s\n", name)
	}
	b.WriteString("      - DIRECT\n\nrules:\n  - MATCH,节点选择\n")
	return b.String(), nil
}

func endpointName(server string, port int) string {
	return strings.NewReplacer(".", "-", ":", "-", "_", "-").Replace(server) + fmt.Sprintf("-%d", port)
}
