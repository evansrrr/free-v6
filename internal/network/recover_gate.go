package network

import "time"

// hotspotRecoverWindow is how fresh a leftover snapshot must be before the
// self-heal is allowed to actively start tethering again. A crash right after
// a switch should recover; a snapshot the user last wrote days ago (or one
// written by a pre-upgrade build, zero SavedAt) must only restore roles, never
// silently switch the user's hotspot back on.
const hotspotRecoverWindow = 30 * time.Minute

// shouldRecoverTethering gates the "start tethering when it is off" path in the
// leftover self-heal. It must not resurrect a hotspot the user turned off
// mid-session, so it only fires for a winrt+Applied snapshot that was saved
// very recently (last save ≈ the switch that left the machine in this state).
func shouldRecoverTethering(meta *HotspotMeta, savedAt, now time.Time) bool {
	if meta == nil || !meta.Applied || meta.Path != "winrt" {
		return false
	}
	if savedAt.IsZero() {
		return false
	}
	return now.Sub(savedAt) <= hotspotRecoverWindow
}
