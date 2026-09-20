package network

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

type RestoreCommand struct {
	Executable string
	Args       []string
}

func SaveSnapshot(path string, snapshot Snapshot) error {
	if path == "" {
		return fmt.Errorf("network snapshot path is empty")
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return fmt.Errorf("create network snapshot directory: %w", err)
	}
	data, err := json.MarshalIndent(snapshot, "", "  ")
	if err != nil {
		return fmt.Errorf("encode network snapshot: %w", err)
	}
	if err := os.WriteFile(path, append(data, '\n'), 0o600); err != nil {
		return fmt.Errorf("write network snapshot: %w", err)
	}
	return nil
}

func LoadSnapshot(path string) (Snapshot, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return Snapshot{}, fmt.Errorf("read network snapshot: %w", err)
	}
	var snapshot Snapshot
	if err := json.Unmarshal(data, &snapshot); err != nil {
		return Snapshot{}, fmt.Errorf("decode network snapshot: %w", err)
	}
	return snapshot, nil
}

func BuildRestoreCommands(snapshot Snapshot) []RestoreCommand {
	commands := make([]RestoreCommand, 0, len(snapshot.Interfaces)*2)
	for _, interfaceSnapshot := range snapshot.Interfaces {
		if interfaceSnapshot.Alias == "" {
			continue
		}
		commands = append(commands, buildDNSRestoreCommand("ipv4", interfaceSnapshot.Alias, interfaceSnapshot.IPv4DNSServers)...)
		commands = append(commands, buildDNSRestoreCommand("ipv6", interfaceSnapshot.Alias, interfaceSnapshot.IPv6DNSServers)...)
	}
	return commands
}

func buildDNSRestoreCommand(protocol, alias string, servers []string) []RestoreCommand {
	args := []string{"interface", protocol, "set", "dnsservers", "name=" + alias}
	if len(servers) == 0 {
		args = append(args, "source=dhcp")
		return []RestoreCommand{{Executable: "netsh", Args: args}}
	}
	args = append(args, "source=static", "address="+servers[0], "validate=no")
	commands := []RestoreCommand{{Executable: "netsh", Args: args}}
	for index, server := range servers[1:] {
		commands = append(commands, RestoreCommand{
			Executable: "netsh",
			Args:       []string{"interface", protocol, "add", "dnsservers", "name=" + alias, "address=" + server, "index=" + fmt.Sprint(index+2), "validate=no"},
		})
	}
	return commands
}
