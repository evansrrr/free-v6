package main

import "testing"

// netstat rows: only the LISTENING row of the exact address may match — not
// the server side of an accepted connection on the same port (different pid
// here to prove the state filter), not other ports, not IPv6.
func TestListeningPIDPicksOnlyTheListener(t *testing.T) {
	out := "  TCP    127.0.0.1:13335        127.0.0.1:52345        ESTABLISHED     7777\n" +
		"  TCP    127.0.0.1:52344        127.0.0.1:13335        ESTABLISHED     8888\n" +
		"  TCP    127.0.0.1:13335        0.0.0.0:0              LISTENING       95096\n" +
		"  TCP    [::]:13335             [::]:0                 LISTENING       4242\n"
	if pid := listeningPID(out, "127.0.0.1:13335"); pid != 95096 {
		t.Fatalf("want listener pid 95096, got %d", pid)
	}
	if pid := listeningPID(out, "127.0.0.1:9999"); pid != 0 {
		t.Fatalf("no listener on a different port, got %d", pid)
	}
	if pid := listeningPID("", "127.0.0.1:13335"); pid != 0 {
		t.Fatalf("empty netstat output must yield 0, got %d", pid)
	}
}

// The shell reserves a free port per launch; anything unusable must fall back
// to the historical fixed address instead of binding port 0 or a bad port.
func TestListenAddressHonoursEnvPort(t *testing.T) {
	t.Setenv("FREEV6_API_PORT", "23456")
	if got := listenAddress(); got != "127.0.0.1:23456" {
		t.Fatalf("valid port: got %q", got)
	}
	for _, raw := range []string{"", "0", "-1", "65536", "nope", "  "} {
		t.Setenv("FREEV6_API_PORT", raw)
		if got := listenAddress(); got != defaultListenAddress {
			t.Fatalf("port %q: want fallback %q, got %q", raw, defaultListenAddress, got)
		}
	}
}
