// normalize.go — the filing label: DefaultNormalizer collapses per-entity
// path segments into one endpoint.

package wirelog

import "strings"

// hexSegMin is the shortest all-hex segment treated as an identifier (B14 "long hex").
const hexSegMin = 16

// DefaultNormalizer replaces UUID, all-numeric, and long-hex path segments
// with "{id}" so per-entity URLs collapse into one endpoint; everything else
// is left untouched.
func DefaultNormalizer(path string) string {
	if !strings.Contains(path, "/") {
		return path
	}
	segs := strings.Split(path, "/")
	changed := false
	for i, s := range segs {
		if isUUIDSeg(s) || isNumericSeg(s) || isLongHexSeg(s) {
			segs[i] = "{id}"
			changed = true
		}
	}
	if !changed {
		return path
	}
	return strings.Join(segs, "/")
}

// isUUIDSeg matches the canonical 8-4-4-4-12 hex form.
func isUUIDSeg(s string) bool {
	if len(s) != 36 {
		return false
	}
	for i := range 36 {
		switch i {
		case 8, 13, 18, 23:
			if s[i] != '-' {
				return false
			}
		default:
			if !isHexByte(s[i]) {
				return false
			}
		}
	}
	return true
}

func isNumericSeg(s string) bool {
	if s == "" {
		return false
	}
	for i := 0; i < len(s); i++ {
		if s[i] < '0' || s[i] > '9' {
			return false
		}
	}
	return true
}

func isLongHexSeg(s string) bool {
	if len(s) < hexSegMin {
		return false
	}
	for i := 0; i < len(s); i++ {
		if !isHexByte(s[i]) {
			return false
		}
	}
	return true
}

func isHexByte(c byte) bool {
	return (c >= '0' && c <= '9') || (c >= 'a' && c <= 'f') || (c >= 'A' && c <= 'F')
}
