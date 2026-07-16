package wirelog

import (
	"reflect"
	"testing"
)

var wantMaskDefaults = []string{
	"msisdn", "phone", "phone_number", "mobile", "account_number",
	"account_name", "iban", "bvn", "nin", "pin", "otp", "cvv", "pan",
	"password", "secret", "token", "access_token", "refresh_token",
	"api_key", "first_name", "last_name", "address", "email",
	"receiver_account", "receiver_account_number", "sender_phone_number",
}

func TestNewConfigDefaults(t *testing.T) {
	cfg := NewConfig("magma")
	if cfg.Provider != "magma" {
		t.Errorf("Provider = %q, want %q", cfg.Provider, "magma")
	}
	if cfg.CaptureBodies {
		t.Error("CaptureBodies defaults true, must default false")
	}
	if cfg.MaxBodyBytes != 16384 {
		t.Errorf("MaxBodyBytes = %d, want 16384", cfg.MaxBodyBytes)
	}
	if !reflect.DeepEqual(cfg.MaskFields, wantMaskDefaults) {
		t.Errorf("MaskFields = %v, want %v", cfg.MaskFields, wantMaskDefaults)
	}
	if want := []string{"/oauth", "/token", "/auth"}; !reflect.DeepEqual(cfg.SkipBodyPaths, want) {
		t.Errorf("SkipBodyPaths = %v, want %v", cfg.SkipBodyPaths, want)
	}
	if want := []string{"/health", "/ping", "/status"}; !reflect.DeepEqual(cfg.ExcludePaths, want) {
		t.Errorf("ExcludePaths = %v, want %v", cfg.ExcludePaths, want)
	}
	if cfg.PathNormalizer == nil {
		t.Fatal("PathNormalizer is nil, want DefaultNormalizer")
	}
	if got := cfg.PathNormalizer("/users/123"); got != "/users/{id}" {
		t.Errorf("PathNormalizer(/users/123) = %q, want /users/{id}", got)
	}
}

func TestWithExtraMaskFieldsAppends(t *testing.T) {
	cfg := NewConfig("magma", WithExtraMaskFields("sender_first_name", "sender_last_name"))
	want := append(append([]string(nil), wantMaskDefaults...), "sender_first_name", "sender_last_name")
	if !reflect.DeepEqual(cfg.MaskFields, want) {
		t.Errorf("MaskFields = %v, want defaults + extras", cfg.MaskFields)
	}
}

func TestConfigsDoNotShareDefaultBacking(t *testing.T) {
	a := NewConfig("a", WithExtraMaskFields("x"))
	b := NewConfig("b")
	if !reflect.DeepEqual(b.MaskFields, wantMaskDefaults) {
		t.Errorf("second config's MaskFields = %v, polluted by first config's append", b.MaskFields)
	}
	a.MaskFields[0] = "mutated"
	if defaultMaskFields[0] != "msisdn" {
		t.Error("mutating a minted config leaked into defaultMaskFields")
	}
}

func TestRemainingConfigOptions(t *testing.T) {
	m := func(field string, value any) any { return "X" }
	cfg := NewConfig("magma",
		WithCaptureBodies(true),
		WithExtraExcludePaths("/metrics"),
		WithExtraSkipBodyPaths("/secrets"),
		WithMasker(m),
	)
	if !cfg.CaptureBodies {
		t.Error("WithCaptureBodies(true) not applied")
	}
	if got := cfg.ExcludePaths[len(cfg.ExcludePaths)-1]; got != "/metrics" {
		t.Errorf("ExcludePaths last = %q, want /metrics", got)
	}
	if got := cfg.SkipBodyPaths[len(cfg.SkipBodyPaths)-1]; got != "/secrets" {
		t.Errorf("SkipBodyPaths last = %q, want /secrets", got)
	}
	if cfg.Masker == nil || cfg.Masker("f", nil) != "X" {
		t.Error("WithMasker not applied")
	}
}
