package mihomo

import "testing"

func TestParseCoreVersion(t *testing.T) {
	cases := []struct {
		raw     string
		valid   bool
		legacy  bool
		parts   [3]int
		pre     string
		display string
	}{
		{"core-v0.1.0-rc.2", true, false, [3]int{0, 1, 0}, "rc.2", "v0.1.0-rc.2"},
		{"core-v1.2.3", true, false, [3]int{1, 2, 3}, "", "v1.2.3"},
		{"  CORE-V0.4.0  ", true, false, [3]int{0, 4, 0}, "", "v0.4.0"},
		{"v1.19.31", true, true, [3]int{1, 19, 31}, "", "v1.19.31"},
		{"1.10.0", true, true, [3]int{1, 10, 0}, "", "1.10.0"},
		{"", false, false, [3]int{}, "", ""},
		{"core-v", false, false, [3]int{}, "", ""},
		{"core-v1.2.3.4", false, false, [3]int{}, "", ""},
		{"core-vX.Y.Z", false, false, [3]int{}, "", ""},
	}
	for _, testCase := range cases {
		version := ParseCoreVersion(testCase.raw)
		if version.Valid() != testCase.valid {
			t.Fatalf("%q: valid = %v, want %v", testCase.raw, version.Valid(), testCase.valid)
		}
		if !testCase.valid {
			continue
		}
		if version.Legacy() != testCase.legacy {
			t.Fatalf("%q: legacy = %v, want %v", testCase.raw, version.Legacy(), testCase.legacy)
		}
		if version.parts != testCase.parts || version.pre != testCase.pre {
			t.Fatalf("%q: parsed %v/%q, want %v/%q", testCase.raw, version.parts, version.pre, testCase.parts, testCase.pre)
		}
		if got := version.Display(); got != testCase.display {
			t.Fatalf("%q: Display = %q, want %q", testCase.raw, got, testCase.display)
		}
	}
}

func TestCompareCoreVersion(t *testing.T) {
	cases := []struct {
		left, right string
		want        int
	}{
		{"core-v0.1.0", "core-v0.1.0", 0},
		{"core-v0.1.0", "core-v0.2.0", -1},
		{"core-v0.2.0", "core-v0.1.9", 1},
		// 正式版高于同号预发布。
		{"core-v0.1.0", "core-v0.1.0-rc.2", 1},
		{"core-v0.1.0-rc.2", "core-v0.1.0-rc.10", -1},
		{"core-v0.1.0-rc.2", "core-v0.1.0-beta.1", 1},
		// 遗留上游内核永远更旧，无论数字多大。
		{"v1.19.31", "core-v0.1.0", -1},
		{"core-v0.1.0", "v1.19.31", 1},
		{"v1.10.0", "v1.19.31", -1},
		// 空版本（未安装）最低。
		{"", "core-v0.1.0", -1},
		{"core-v0.1.0", "", 1},
	}
	for _, testCase := range cases {
		got := ParseCoreVersion(testCase.left).Compare(ParseCoreVersion(testCase.right))
		if got != testCase.want {
			t.Fatalf("compare(%q, %q) = %d, want %d", testCase.left, testCase.right, got, testCase.want)
		}
	}
}

func TestProbeVersion(t *testing.T) {
	output := "Mihomo Meta core-v0.1.0-rc.2 windows amd64 with go1.26.3 2026-11-16T16:20:20Z\nUse tags: with_gvisor\n"
	version := ProbeVersion(output)
	if version.raw != "core-v0.1.0-rc.2" {
		t.Fatalf("probe parsed %q", version.raw)
	}

	legacy := ProbeVersion("Mihomo Meta 1.19.31 windows amd64 with go1.24.0\n")
	if !legacy.Legacy() {
		t.Fatalf("upstream version should be flagged legacy: %+v", legacy)
	}

	if ProbeVersion("garbage output\n").Valid() {
		t.Fatal("garbage must not parse")
	}
}
