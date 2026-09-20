package mihomo

import "testing"

func TestSelectWindowsAMD64Asset(t *testing.T) {
	release := releaseInfo{TagName: "alpha-test", Assets: []releaseAsset{
		{Name: "mihomo-linux-amd64-alpha.zip"},
		{Name: "mihomo-windows-amd64-alpha-compatible.zip", URL: "https://example.invalid/runtime.zip"},
	}}
	asset, err := SelectWindowsAMD64Asset(release)
	if err != nil {
		t.Fatal(err)
	}
	if asset.Name != "mihomo-windows-amd64-alpha-compatible.zip" {
		t.Fatalf("unexpected asset %q", asset.Name)
	}
}

func TestSelectWindowsAMD64AssetRejectsStableOnly(t *testing.T) {
	_, err := SelectWindowsAMD64Asset(releaseInfo{TagName: "stable", Assets: []releaseAsset{{Name: "mihomo-windows-amd64.zip"}}})
	if err == nil {
		t.Fatal("expected stable-only release to be rejected")
	}
}
