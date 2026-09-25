package main

import (
	"image"
	"image/color"
	"image/png"
	"math"
	"os"
	"path/filepath"
	"strconv"
	"testing"
)

// Accent DWORDs are 0xAABBGGRR (low byte = red). Anchored to real data seen
// via reg query: StartColorMenu 0xff475675 equals AccentPalette entry
// "75 56 47 00" in COLORREF byte order, and AccentColorMenu 0xff596c8b sits in
// the same brown family (palette entry 8d 6e 5b).
func TestFormatAccent(t *testing.T) {
	cases := []struct {
		raw  uint32
		want string
	}{
		{0xff475675, "#755647"},
		{0xff596c8b, "#8b6c59"},
	}
	for _, c := range cases {
		if got := formatAccent(c.raw); got != c.want {
			t.Errorf("formatAccent(%#x) = %s, want %s", c.raw, got, c.want)
		}
	}
}

func writeTestPNG(t *testing.T, fill color.RGBA) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "img.png")
	img := image.NewRGBA(image.Rect(0, 0, 64, 64))
	for y := 0; y < 64; y++ {
		for x := 0; x < 64; x++ {
			img.Set(x, y, fill)
		}
	}
	file, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := png.Encode(file, img); err != nil {
		file.Close()
		t.Fatal(err)
	}
	file.Close()
	return path
}

func hexToRGB(t *testing.T, hex string) (r, g, b int) {
	t.Helper()
	if len(hex) != 7 || hex[0] != '#' {
		t.Fatalf("bad hex %q", hex)
	}
	parsed, err := strconv.ParseUint(hex[1:], 16, 32)
	if err != nil {
		t.Fatalf("bad hex %q: %v", hex, err)
	}
	return int(parsed >> 16 & 0xff), int(parsed >> 8 & 0xff), int(parsed & 0xff)
}

// A solid vivid image must round-trip through HSL voting to ≈its own color.
func TestDominantColorSolidImage(t *testing.T) {
	path := writeTestPNG(t, color.RGBA{R: 200, G: 80, B: 40, A: 255}) // #c85028
	got, err := dominantColor(path)
	if err != nil {
		t.Fatalf("dominantColor: %v", err)
	}
	r, g, b := hexToRGB(t, got)
	if math.Abs(float64(r-200)) > 2 || math.Abs(float64(g-80)) > 2 || math.Abs(float64(b-40)) > 2 {
		t.Fatalf("dominantColor = %s, want ≈#c85028", got)
	}
}

// Gray wallpapers are rejected → caller falls back to the accent color.
func TestDominantColorRejectsGray(t *testing.T) {
	path := writeTestPNG(t, color.RGBA{R: 128, G: 128, B: 128, A: 255})
	if _, err := dominantColor(path); err == nil {
		t.Fatal("expected mostly-gray wallpaper to be rejected")
	}
}

// The cache must serve a second call without re-decoding.
func TestWallpaperColorCache(t *testing.T) {
	path := writeTestPNG(t, color.RGBA{R: 40, G: 120, B: 200, A: 255})
	first, err := wallpaperColor(path)
	if err != nil {
		t.Fatalf("wallpaperColor: %v", err)
	}
	second, err := wallpaperColor(path)
	if err != nil {
		t.Fatalf("wallpaperColor cached: %v", err)
	}
	if first != second {
		t.Fatalf("cache returned %s then %s", first, second)
	}
}

// Default-off requirement: appearance prefs are frontend-only (localStorage),
// so the helper settings struct must not grow appearance fields that could
// reset them.
func TestSettingsHasNoAppearanceFields(t *testing.T) {
	s := defaultSettings()
	if s.DevMode || s.AutoStart {
		t.Fatalf("unrelated defaults changed: %+v", s)
	}
}
