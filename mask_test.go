package wirelog

import (
	"encoding/json"
	"net/http"
	"reflect"
	"strings"
	"testing"
)

// TestMaskHeadersBuiltinAndCustomDeny checks the built-in denylist plus
// Config.DenyHeaders extras all mask, while other headers pass through.
func TestMaskHeadersBuiltinAndCustomDeny(t *testing.T) {
	deny := denyHeaderSet([]string{"X-Custom-Secret"})
	src := http.Header{
		"Authorization":   {"Bearer abc"},
		"Cookie":          {"session=1", "theme=dark"},
		"X-Api-Key":       {"key123"},
		"X-Custom-Secret": {"shh"},
		"Content-Type":    {"application/json"},
	}
	got := maskHeaders(src, deny)
	want := map[string][]string{
		"Authorization":   {maskedValue},
		"Cookie":          {maskedValue},
		"X-Api-Key":       {maskedValue},
		"X-Custom-Secret": {maskedValue},
		"Content-Type":    {"application/json"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("maskHeaders = %v, want %v", got, want)
	}
}

// TestMaskHeadersDoesNotMutateSource checks masking copies, and the
// copy shares no value slices with the source map.
func TestMaskHeadersDoesNotMutateSource(t *testing.T) {
	src := http.Header{"Authorization": {"Bearer abc"}, "Accept": {"*/*"}}
	_ = maskHeaders(src, denyHeaderSet(nil))
	if src.Get("Authorization") != "Bearer abc" {
		t.Error("source header map was mutated")
	}
	got := maskHeaders(src, denyHeaderSet(nil))
	got["Accept"][0] = "mutated"
	if src["Accept"][0] != "*/*" {
		t.Error("masked copy shares value slices with the source")
	}
}

// TestMaskHeadersCaseInsensitive confirms deny matching ignores header casing.
func TestMaskHeadersCaseInsensitive(t *testing.T) {
	src := http.Header{}
	src["AUTHORIZATION"] = []string{"Bearer abc"} // bypass canonicalization
	src["x-signature"] = []string{"sig"}
	got := maskHeaders(src, denyHeaderSet(nil))
	if got["AUTHORIZATION"][0] != maskedValue || got["x-signature"][0] != maskedValue {
		t.Errorf("case-insensitive deny failed: %v", got)
	}
}

// TestMaskHeadersEmptyIsNil checks empty input maps to nil so the jsonb
// column stores NULL.
func TestMaskHeadersEmptyIsNil(t *testing.T) {
	if got := maskHeaders(nil, denyHeaderSet(nil)); got != nil {
		t.Errorf("maskHeaders(nil) = %v, want nil", got)
	}
	if got := maskHeaders(http.Header{}, denyHeaderSet(nil)); got != nil {
		t.Errorf("maskHeaders(empty) = %v, want nil", got)
	}
}

// maskJSON runs maskBody with the default mask list and decodes the result,
// failing the test if the output is not valid JSON.
func maskJSON(t *testing.T, body string, maxBytes int, m Masker) map[string]any {
	t.Helper()
	out := maskBody([]byte(body), maxBytes, maskFieldSet(defaultMaskFields), m)
	var v map[string]any
	if err := json.Unmarshal(out, &v); err != nil {
		t.Fatalf("maskBody output is not valid JSON: %v (%s)", err, out)
	}
	return v
}

// TestMaskBodyNestedObjectsAndArrays checks masking reaches fields nested in
// objects and arrays while leaving unmatched siblings untouched.
func TestMaskBodyNestedObjectsAndArrays(t *testing.T) {
	body := `{
		"amount": 100,
		"msisdn": "+237670000001",
		"receiver": {"account_number": "0123456789", "bank": "GTB"},
		"parties": [{"phone": "+237670000002", "role": "sender"}]
	}`
	v := maskJSON(t, body, 16384, nil)
	if v["msisdn"] != maskedValue {
		t.Errorf("msisdn = %v, want masked", v["msisdn"])
	}
	if v["amount"] != float64(100) {
		t.Errorf("amount = %v, want 100 untouched", v["amount"])
	}
	recv := v["receiver"].(map[string]any)
	if recv["account_number"] != maskedValue || recv["bank"] != "GTB" {
		t.Errorf("receiver = %v, want account_number masked, bank untouched", recv)
	}
	party := v["parties"].([]any)[0].(map[string]any)
	if party["phone"] != maskedValue || party["role"] != "sender" {
		t.Errorf("party = %v, want phone masked, role untouched", party)
	}
}

// TestMaskBodyCaseInsensitiveKeys confirms body keys match the mask list
// regardless of casing.
func TestMaskBodyCaseInsensitiveKeys(t *testing.T) {
	v := maskJSON(t, `{"MSISDN": "+237670000001", "Account_Number": "01234"}`, 16384, nil)
	if v["MSISDN"] != maskedValue || v["Account_Number"] != maskedValue {
		t.Errorf("case-insensitive key match failed: %v", v)
	}
}

// TestMaskBodyMatchedSubtreeReplacedWholesale checks a matched key's
// entire value is replaced, with no recursion into the matched subtree.
func TestMaskBodyMatchedSubtreeReplacedWholesale(t *testing.T) {
	body := `{"token": {"access": "a", "refresh": {"deep": "b"}}, "keep": {"token": "x"}}`
	v := maskJSON(t, body, 16384, nil)
	if v["token"] != maskedValue {
		t.Errorf("matched subtree = %v, want value replaced entirely", v["token"])
	}
	if v["keep"].(map[string]any)["token"] != maskedValue {
		t.Errorf("nested match inside unmatched parent not masked: %v", v["keep"])
	}
}

// TestMaskBodyCustomMasker checks a custom Masker receives the lowercased
// field name and its return value replaces the field.
func TestMaskBodyCustomMasker(t *testing.T) {
	var seenField string
	m := func(field string, value any) any {
		seenField = field
		s, _ := value.(string)
		if len(s) > 4 {
			return "…" + s[len(s)-4:]
		}
		return "…"
	}
	v := maskJSON(t, `{"MSISDN": "+237670000001"}`, 16384, m)
	if seenField != "msisdn" {
		t.Errorf("Masker field = %q, want lowercased msisdn", seenField)
	}
	if v["MSISDN"] != "…0001" {
		t.Errorf("MSISDN = %v, want custom-masked …0001", v["MSISDN"])
	}
}

// TestMaskBodyNonJSONWrap checks non-JSON bodies wrap as {"_raw": ...}
// without a spurious truncation marker.
func TestMaskBodyNonJSONWrap(t *testing.T) {
	out := maskBody([]byte("plain text, not json"), 16384, maskFieldSet(nil), nil)
	var v map[string]any
	if err := json.Unmarshal(out, &v); err != nil {
		t.Fatalf("wrap is not valid JSON: %v", err)
	}
	if v["_raw"] != "plain text, not json" {
		t.Errorf("_raw = %v, want original text", v["_raw"])
	}
	if _, present := v["_truncated"]; present {
		t.Error("_truncated present on an untruncated body")
	}
}

// TestMaskBodyTruncationMarker checks bytes are cut BEFORE parsing and the
// wrap carries _truncated plus exactly the first maxBytes bytes.
func TestMaskBodyTruncationMarker(t *testing.T) {
	body := `{"data": "` + strings.Repeat("x", 100) + `"}`
	out := maskBody([]byte(body), 20, maskFieldSet(nil), nil)
	var v map[string]any
	if err := json.Unmarshal(out, &v); err != nil {
		t.Fatalf("wrap is not valid JSON: %v", err)
	}
	if v["_truncated"] != true {
		t.Errorf("_truncated = %v, want true", v["_truncated"])
	}
	if v["_raw"] != body[:20] {
		t.Errorf("_raw = %q, want first 20 bytes %q", v["_raw"], body[:20])
	}
}

// TestMaskBodyEmptyIsNil checks empty bodies produce nil so the jsonb column
// stores NULL.
func TestMaskBodyEmptyIsNil(t *testing.T) {
	if out := maskBody(nil, 16384, maskFieldSet(nil), nil); out != nil {
		t.Errorf("maskBody(nil) = %s, want nil", out)
	}
	if out := maskBody([]byte{}, 16384, maskFieldSet(nil), nil); out != nil {
		t.Errorf("maskBody(empty) = %s, want nil", out)
	}
}

// TestMaskBodyUnmarshalableMaskerResultRemasks checks a Masker returning an
// unmarshalable value falls back to constant masking, never raw bytes.
func TestMaskBodyUnmarshalableMaskerResultRemasks(t *testing.T) {
	m := func(field string, value any) any { return make(chan int) }
	out := maskBody([]byte(`{"msisdn": "+237670000001"}`), 16384, maskFieldSet(defaultMaskFields), m)
	var v map[string]any
	if err := json.Unmarshal(out, &v); err != nil {
		t.Fatalf("fallback is not valid JSON: %v", err)
	}
	if v["msisdn"] != maskedValue {
		t.Errorf("msisdn = %v, want constant-masked fallback, never raw", v["msisdn"])
	}
}
