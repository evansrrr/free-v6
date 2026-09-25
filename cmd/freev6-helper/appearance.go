package main

import (
	"fmt"
	"image"
	_ "image/gif"
	_ "image/jpeg"
	_ "image/png"
	"math"
	"net/http"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"sync"
)

// Registry sources for the dynamic-color seed: the desktop wallpaper first,
// the Windows accent color as fallback (both read live, nothing persisted).
const (
	appearanceAccentKey   = `HKCU\Software\Microsoft\Windows\CurrentVersion\Explorer\Accent`
	appearanceAccentValue = "AccentColorMenu"
	appearanceDeskKey     = `HKCU\Control Panel\Desktop`
	appearanceWallValue   = "WallPaper"
)

func regQuery(key, value string) (string, error) {
	output, err := exec.Command("reg", "query", key, "/v", value).CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("reg query %s: %s", key, strings.TrimSpace(string(output)))
	}
	return string(output), nil
}

// regReadString extracts the data of a REG_SZ / REG_EXPAND_SZ value.
func regReadString(key, value string) (string, error) {
	output, err := regQuery(key, value)
	if err != nil {
		return "", err
	}
	for _, line := range strings.Split(output, "\n") {
		fields := strings.Fields(line)
		for i, field := range fields {
			if field == "REG_SZ" || field == "REG_EXPAND_SZ" {
				return strings.Join(fields[i+1:], " "), nil
			}
		}
	}
	return "", fmt.Errorf("value %q not found under %s", value, key)
}

// regReadDWORD parses a REG_DWORD value such as "0xff596c8b".
func regReadDWORD(key, value string) (uint32, error) {
	output, err := regQuery(key, value)
	if err != nil {
		return 0, err
	}
	for _, line := range strings.Split(output, "\n") {
		fields := strings.Fields(line)
		for i, field := range fields {
			if field == "REG_DWORD" && i+1 < len(fields) {
				parsed, parseErr := strconv.ParseUint(fields[i+1], 0, 32)
				if parseErr != nil {
					return 0, fmt.Errorf("parse %q: %w", fields[i+1], parseErr)
				}
				return uint32(parsed), nil
			}
		}
	}
	return 0, fmt.Errorf("value %q not found under %s", value, key)
}

// formatAccent renders a Windows accent DWORD (0xAABBGGRR, low byte = red).
// Verified against AccentPalette on this machine: StartColorMenu 0xff475675
// equals palette entry "75 56 47 00" (COLORREF byte order) → #755647.
func formatAccent(raw uint32) string {
	return fmt.Sprintf("#%02x%02x%02x", raw&0xff, (raw>>8)&0xff, (raw>>16)&0xff)
}

func accentColor() (string, error) {
	raw, err := regReadDWORD(appearanceAccentKey, appearanceAccentValue)
	if err != nil {
		return "", err
	}
	return formatAccent(raw), nil
}

// wallpaperColor caches dominantColor per (path, mtime, size) so the status UI
// does not re-decode a 4K wallpaper on every palette re-derive.
var wallpaperColorCache struct {
	sync.Mutex
	path  string
	mod   int64
	size  int64
	color string
}

func wallpaperColor(path string) (string, error) {
	info, err := os.Stat(path)
	if err != nil {
		return "", err
	}
	wallpaperColorCache.Lock()
	defer wallpaperColorCache.Unlock()
	if wallpaperColorCache.path == path &&
		wallpaperColorCache.mod == info.ModTime().UnixNano() &&
		wallpaperColorCache.size == info.Size() &&
		wallpaperColorCache.color != "" {
		return wallpaperColorCache.color, nil
	}
	color, err := dominantColor(path)
	if err != nil {
		return "", err
	}
	wallpaperColorCache.path = path
	wallpaperColorCache.mod = info.ModTime().UnixNano()
	wallpaperColorCache.size = info.Size()
	wallpaperColorCache.color = color
	return color, nil
}

// dominantColor samples the image and votes on hue (weighted by saturation),
// saturation and mid-luminance. Near-black/near-white pixels are ignored, and
// wallpapers without meaningful chroma are rejected so the accent fallback can
// take over (rejection uses average saturation of the usable pixels — bright
// skies/backgrounds must not count against it).
func dominantColor(path string) (string, error) {
	file, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer file.Close()
	img, _, err := image.Decode(file)
	if err != nil {
		return "", fmt.Errorf("decode wallpaper: %w", err)
	}
	bounds := img.Bounds()
	width, height := bounds.Dx(), bounds.Dy()
	if width < 4 || height < 4 {
		return "", fmt.Errorf("wallpaper too small: %dx%d", width, height)
	}
	// ~200x200 samples max keeps 4K wallpapers cheap
	step := int(math.Sqrt(float64(width*height) / 40000.0))
	if step < 1 {
		step = 1
	}

	var sumSin, sumCos, sumSat, sumSatWeight, sumLumWeighted, sumLumWeight, sumS float64
	var kept int
	for y := bounds.Min.Y; y < bounds.Max.Y; y += step {
		for x := bounds.Min.X; x < bounds.Max.X; x += step {
			r, g, b, _ := img.At(x, y).RGBA()
			h, s, l := rgbToHSL(float64(r>>8), float64(g>>8), float64(b>>8))
			if l < 0.10 || l > 0.97 {
				continue
			}
			kept++
			sumS += s
			if s > 0.10 {
				rad := h * math.Pi / 180
				weight := s * s
				sumSin += math.Sin(rad) * weight
				sumCos += math.Cos(rad) * weight
				sumSat += s * weight
				sumSatWeight += weight
			}
			lw := l * (1 - l) // mid-tones vote strongest for luminance
			sumLumWeighted += l * lw
			sumLumWeight += lw
		}
	}
	if kept < 16 {
		return "", fmt.Errorf("wallpaper has too few usable pixels")
	}
	if avgS := sumS / float64(kept); avgS < 0.05 || sumSatWeight == 0 {
		return "", fmt.Errorf("wallpaper is mostly gray (avg saturation %.3f)", avgS)
	}
	hue := math.Atan2(sumSin, sumCos) * 180 / math.Pi
	if hue < 0 {
		hue += 360
	}
	lum := 0.5
	if sumLumWeight > 0 {
		lum = sumLumWeighted / sumLumWeight
	}
	return hslToHex(hue, sumSat/sumSatWeight, lum), nil
}

func rgbToHSL(r, g, b float64) (h, s, l float64) {
	r, g, b = r/255, g/255, b/255
	max := math.Max(r, math.Max(g, b))
	min := math.Min(r, math.Min(g, b))
	l = (max + min) / 2
	if max == min {
		return 0, 0, l
	}
	delta := max - min
	if l > 0.5 {
		s = delta / (2 - max - min)
	} else {
		s = delta / (max + min)
	}
	switch max {
	case r:
		h = (g - b) / delta
		if g < b {
			h += 6
		}
	case g:
		h = (b-r)/delta + 2
	default:
		h = (r-g)/delta + 4
	}
	h *= 60
	return h, s, l
}

func hslToHex(h, s, l float64) string {
	if s == 0 {
		value := uint8(math.Round(l * 255))
		return fmt.Sprintf("#%02x%02x%02x", value, value, value)
	}
	q := l
	if l < 0.5 {
		q = l * (1 + s)
	} else {
		q = l + s - l*s
	}
	p := 2*l - q
	hh := h / 360
	hue := func(t float64) float64 {
		if t < 0 {
			t += 1
		}
		if t > 1 {
			t -= 1
		}
		if t < 1.0/6 {
			return p + (q-p)*6*t
		}
		if t < 1.0/2 {
			return q
		}
		if t < 2.0/3 {
			return p + (q-p)*(2.0/3-t)*6
		}
		return p
	}
	return fmt.Sprintf("#%02x%02x%02x",
		uint8(math.Round(hue(hh+1.0/3)*255)),
		uint8(math.Round(hue(hh)*255)),
		uint8(math.Round(hue(hh-1.0/3)*255)),
	)
}

// appearance resolves the dynamic-color seed: wallpaper dominant color first,
// Windows accent color as fallback. GET /api/v1/appearance
func (h *helper) appearance(writer http.ResponseWriter, request *http.Request) {
	if request.Method != http.MethodGet {
		writeJSON(writer, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
		return
	}
	accent, accentErr := accentColor()
	color, source := "", ""
	if path, pathErr := regReadString(appearanceDeskKey, appearanceWallValue); pathErr == nil && path != "" {
		if wallColor, wallErr := wallpaperColor(path); wallErr == nil {
			color, source = wallColor, "wallpaper"
		}
	}
	if color == "" && accentErr == nil {
		color, source = accent, "accent"
	}
	writeJSON(writer, http.StatusOK, map[string]any{
		"ok":     true,
		"source": source,
		"color":  color,
		"accent": accent,
	})
}
