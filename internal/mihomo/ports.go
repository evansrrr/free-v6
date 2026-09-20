package mihomo

import (
	"fmt"
	"net"
)

type ListenPort struct {
	Network string
	Address string
	Name    string
}

var requiredListenPorts = []ListenPort{
	{Network: "tcp", Address: "127.0.0.1:7890", Name: "mixed proxy"},
	{Network: "tcp", Address: "127.0.0.1:9090", Name: "controller"},
	{Network: "tcp", Address: "0.0.0.0:1053", Name: "DNS"},
	{Network: "udp", Address: "0.0.0.0:1053", Name: "DNS"},
}

func CheckListenPorts() error {
	for _, port := range requiredListenPorts {
		listener, err := net.Listen(port.Network, port.Address)
		if err != nil {
			return fmt.Errorf("mihomo %s port %s is unavailable: %w", port.Name, port.Address, err)
		}
		_ = listener.Close()
	}
	return nil
}
