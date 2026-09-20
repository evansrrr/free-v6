package mihomo

import "testing"

func TestRequiredListenPortsAreDefined(t *testing.T) {
	if len(requiredListenPorts) != 4 {
		t.Fatalf("expected four required listen checks, got %d", len(requiredListenPorts))
	}
}
