package mihomo

import "testing"

func TestParseBlacklistText(t *testing.T) {
	list, err := parseBlacklistText("# comment line\n\n  ADS.Example.com \nads.example.com\ntracker.foo.net\r\n")
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"ads.example.com", "tracker.foo.net"} // lowercased + deduped
	if len(list) != len(want) {
		t.Fatalf("expected %v, got %v", want, list)
	}
	for i := range want {
		if list[i] != want[i] {
			t.Fatalf("expected %v, got %v", want, list)
		}
	}

	if _, err := parseBlacklistText("not a domain\n"); err == nil {
		t.Fatal("expected invalid embedded domain error")
	}
}

func TestDefaultBlacklistParses(t *testing.T) {
	// Local builds embed blacklist.txt (may be comment-only); fresh clones
	// embed nothing and get an empty default. Both must parse cleanly.
	if _, err := DefaultBlacklist(); err != nil {
		t.Fatalf("default blacklist must parse: %v", err)
	}
}
