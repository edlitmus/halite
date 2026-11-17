//go:build !linux && !darwin && !(freebsd && (amd64 || arm64))

package metrics

// On every other platform -- Windows, the tier-3 BSDs and illumos, and
// FreeBSD on an architecture whose kinfo_proc layout nobody here has
// checked -- the process families are not registered (see Process).
// The Go families still are.
func residentBytes() (float64, bool) { return 0, false }
func openFDs() (float64, bool)       { return 0, false }
func maxFDs() (float64, bool)        { return 0, false }
