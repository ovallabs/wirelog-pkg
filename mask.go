// mask.go — the redaction desk: header and recursive JSON body masking,
// applied to the captured copy and never to the original.

package wirelog

import (
	"encoding/json"
	"net/http"
	"strings"
)

// maskedValue replaces every denied header and, absent a custom Masker,
// every matched body field.
const maskedValue = "•••"

// builtinDenyHeaders are always masked, case-insensitively, on every record.
var builtinDenyHeaders = []string{
	"authorization", "proxy-authorization", "cookie", "set-cookie",
	"x-api-key", "api-key", "x-auth-token", "x-signature",
}

// denyHeaderSet folds the built-in denylist and Config.DenyHeaders into one
// lowercased lookup set; built once per transport mint, not per request.
func denyHeaderSet(extra []string) map[string]struct{} {
	deny := make(map[string]struct{}, len(builtinDenyHeaders)+len(extra))
	for _, h := range builtinDenyHeaders {
		deny[h] = struct{}{}
	}
	for _, h := range extra {
		deny[strings.ToLower(h)] = struct{}{}
	}
	return deny
}

// maskFieldSet lowercases Config.MaskFields into a lookup set, built once per mint.
func maskFieldSet(fields []string) map[string]struct{} {
	set := make(map[string]struct{}, len(fields))
	for _, f := range fields {
		set[strings.ToLower(f)] = struct{}{}
	}
	return set
}

// maskHeaders returns a masked copy; the source map is never mutated.
// Denied headers always become the mask constant — a custom Masker never
// applies here. Empty input returns nil so the jsonb column maps to NULL.
func maskHeaders(src http.Header, deny map[string]struct{}) map[string][]string {
	if len(src) == 0 {
		return nil
	}
	out := make(map[string][]string, len(src))
	for k, vals := range src {
		if _, denied := deny[strings.ToLower(k)]; denied {
			out[k] = []string{maskedValue}
			continue
		}
		out[k] = append([]string(nil), vals...)
	}
	return out
}

// maskBody truncates to maxBytes BEFORE json.Unmarshal, masks matched
// fields, and returns valid JSON bytes or nil for an empty body.
func maskBody(body []byte, maxBytes int, fields map[string]struct{}, masker Masker) []byte {
	if len(body) == 0 {
		return nil
	}
	truncated := false
	if maxBytes > 0 && len(body) > maxBytes {
		body = body[:maxBytes]
		truncated = true
	}
	var decoded any
	if err := json.Unmarshal(body, &decoded); err != nil {
		return rawWrap(body, truncated)
	}
	masked, err := json.Marshal(maskWalk(decoded, fields, masker))
	if err != nil {
		// custom Masker returned an unmarshalable value — remask with the
		// constant rather than fall back to raw bytes and leak
		masked, _ = json.Marshal(maskWalk(decoded, fields, nil))
	}
	return masked
}

// rawWrap packages non-JSON or broken-by-truncation bytes as valid JSON.
func rawWrap(body []byte, truncated bool) []byte {
	wrapper := map[string]any{"_raw": string(body)}
	if truncated {
		wrapper["_truncated"] = true
	}
	wrapped, _ := json.Marshal(wrapper) // string keys/values never fail to marshal
	return wrapped
}

// maskWalk recurses through decoded JSON; on a key match it replaces the
// VALUE wholesale and never recurses into the matched subtree. The
// value was decoded locally, so in-place mutation is safe.
func maskWalk(value any, fields map[string]struct{}, masker Masker) any {
	switch node := value.(type) {
	case map[string]any:
		for key, child := range node {
			lowerKey := strings.ToLower(key)
			if _, matched := fields[lowerKey]; matched {
				if masker != nil {
					node[key] = masker(lowerKey, child)
				} else {
					node[key] = maskedValue
				}
				continue
			}
			node[key] = maskWalk(child, fields, masker)
		}
	case []any:
		for i, element := range node {
			node[i] = maskWalk(element, fields, masker)
		}
	}
	return value
}
