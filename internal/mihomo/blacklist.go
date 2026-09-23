package mihomo

import (
	"embed"
	"fmt"
	"strings"
)

// blacklist/blacklist.txt holds the default external-domain blacklist. It is
// embedded into freev6-helper at build time so installed copies ship with the
// blocking list out of the box. The file itself is gitignored: fresh clones
// only carry README.md and build with an empty default (readiness is checked
// per file below, the directory pattern always matches).
//
//go:embed all:blacklist
var blacklistFS embed.FS

// DefaultBlacklist returns the blacklist compiled into the binary. When the
// file was not embedded (fresh clone / CI) it returns an empty list, never an
// error; malformed entries in an embedded file fail loudly so a typo cannot
// silently ship.
func DefaultBlacklist() ([]string, error) {
	data, err := blacklistFS.ReadFile("blacklist/blacklist.txt")
	if err != nil {
		return nil, nil // not embedded — build carries no default list
	}
	return parseBlacklistText(string(data))
}

// parseBlacklistText parses the file format: one domain per line, `#`
// comments and blank lines ignored, entries lowercased and deduplicated.
func parseBlacklistText(text string) ([]string, error) {
	var result []string
	seen := make(map[string]bool)
	for _, line := range strings.Split(text, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if err := ValidateDomain(line); err != nil {
			return nil, fmt.Errorf("embedded blacklist: %w", err)
		}
		domain := strings.ToLower(line)
		if seen[domain] {
			continue
		}
		seen[domain] = true
		result = append(result, domain)
	}
	return result, nil
}
