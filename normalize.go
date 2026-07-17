// normalize.go — the filing label: DefaultNormalizer collapses per-entity
// path segments into one endpoint.

package wirelog

import "strings"

// hexSegMin is the shortest all-hex segment treated as an identifier.
const hexSegMin = 16

// DefaultNormalizer replaces UUID, all-numeric, and long-hex path segments
// with "{id}" so per-entity URLs collapse into one endpoint; everything else
// is left untouched.
func DefaultNormalizer(path string) string {
	if !strings.Contains(path, "/") {
		return path
	}
	segments := strings.Split(path, "/")
	changed := false
	for i, segment := range segments {
		if isUUIDSeg(segment) || isNumericSeg(segment) || isLongHexSeg(segment) {
			segments[i] = "{id}"
			changed = true
		}
	}
	if !changed {
		return path
	}
	return strings.Join(segments, "/")
}

// isUUIDSeg reports whether segment is the canonical 8-4-4-4-12 hex UUID form.
func isUUIDSeg(segment string) bool {
	if len(segment) != 36 {
		return false
	}
	for i := range 36 {
		switch i {
		case 8, 13, 18, 23:
			if segment[i] != '-' {
				return false
			}
		default:
			if !isHexByte(segment[i]) {
				return false
			}
		}
	}
	return true
}

// isNumericSeg reports whether segment is non-empty and all decimal digits.
func isNumericSeg(segment string) bool {
	if segment == "" {
		return false
	}
	for i := 0; i < len(segment); i++ {
		if segment[i] < '0' || segment[i] > '9' {
			return false
		}
	}
	return true
}

// isLongHexSeg reports whether segment is at least hexSegMin hex characters.
func isLongHexSeg(segment string) bool {
	if len(segment) < hexSegMin {
		return false
	}
	for i := 0; i < len(segment); i++ {
		if !isHexByte(segment[i]) {
			return false
		}
	}
	return true
}

// isHexByte reports whether char is a hexadecimal digit in either case.
func isHexByte(char byte) bool {
	return (char >= '0' && char <= '9') || (char >= 'a' && char <= 'f') || (char >= 'A' && char <= 'F')
}
