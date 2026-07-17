package wirelog

import "testing"

// TestDefaultNormalizer checks that ID-like path segments collapse to {id}
// and everything else passes through untouched (B14).
func TestDefaultNormalizer(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{"uuid segment", "/v1/transfers/5f0c2f8e-2f5a-4b9e-8a3d-9c1e2b3a4d5f", "/v1/transfers/{id}"},
		{"uppercase uuid", "/v1/transfers/5F0C2F8E-2F5A-4B9E-8A3D-9C1E2B3A4D5F", "/v1/transfers/{id}"},
		{"numeric segment", "/users/12345/accounts/9", "/users/{id}/accounts/{id}"},
		{"long hex segment", "/tx/deadbeefdeadbeef", "/tx/{id}"},
		{"short hex kept", "/tx/deadbeef", "/tx/deadbeef"},
		{"mixed path", "/v2/550e8400-e29b-41d4-a716-446655440000/items/42/abcdef0123456789ab", "/v2/{id}/items/{id}/{id}"},
		{"already clean", "/partner/balance", "/partner/balance"},
		{"words untouched", "/v1/transfers", "/v1/transfers"},
		{"non-hex long segment kept", "/v1/transactionhistory", "/v1/transactionhistory"},
		{"malformed uuid kept", "/v1/5f0c2f8e-2f5a-4b9e-8a3d-9c1e2b3a4d5g", "/v1/5f0c2f8e-2f5a-4b9e-8a3d-9c1e2b3a4d5g"},
		{"empty path", "", ""},
		{"root", "/", "/"},
		{"trailing slash", "/users/123/", "/users/{id}/"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := DefaultNormalizer(tt.in); got != tt.want {
				t.Fatalf("DefaultNormalizer(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}
