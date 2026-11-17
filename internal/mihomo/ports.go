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

// ControllerAddress 是渲染配置里 external-controller 的固定地址。启动时的
// 端口自检与停止时的 TUN 关闭都按它访问内核，两边必须是同一个值。
const ControllerAddress = "127.0.0.1:9090"

var requiredListenPorts = []ListenPort{
	{Network: "tcp", Address: "127.0.0.1:7890", Name: "mixed proxy"},
	{Network: "tcp", Address: ControllerAddress, Name: "controller"},
	{Network: "tcp", Address: "0.0.0.0:1053", Name: "DNS"},
	{Network: "udp", Address: "0.0.0.0:1053", Name: "DNS"},
}

func CheckListenPorts() error {
	for _, port := range requiredListenPorts {
		listener, err := listenPort(port)
		if err != nil {
			return fmt.Errorf("mihomo %s port %s is unavailable: %w", port.Name, port.Address, err)
		}
		_ = listener.Close()
	}
	return nil
}

type closableListener interface {
	Close() error
}

func listenPort(port ListenPort) (closableListener, error) {
	if port.Network == "udp" {
		return net.ListenPacket(port.Network, port.Address)
	}

	listener, err := net.Listen(port.Network, port.Address)
	if err != nil {
		return nil, err
	}
	return listener, nil
}
