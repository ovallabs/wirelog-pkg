package wirelog

import (
	"strings"
	"sync"
	"testing"
)

// TestConfigOptionMutates confirms ConfigOption receives the Config by pointer.
func TestConfigOptionMutates(t *testing.T) {
	var opt ConfigOption = func(c *Config) { c.Provider = "magma" }
	cfg := Config{}
	opt(&cfg)
	if cfg.Provider != "magma" {
		t.Fatalf("Provider = %q, want %q", cfg.Provider, "magma")
	}
}

// TestConfigConcurrentReads checks a minted Config is safe as
// read-only shared state. The race detector is the assertion.
func TestConfigConcurrentReads(t *testing.T) {
	cfg := Config{
		Provider:       "magma",
		Consumer:       "demo",
		CaptureBodies:  true,
		MaxBodyBytes:   16384,
		MaskFields:     []string{"msisdn"},
		DenyHeaders:    []string{"x-custom"},
		Masker:         func(field string, value any) any { return field },
		SkipBodyPaths:  []string{"/oauth"},
		ExcludePaths:   []string{"/health"},
		PathNormalizer: strings.ToLower,
	}
	var wg sync.WaitGroup
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_ = cfg.Provider
			_ = cfg.Consumer
			_ = cfg.CaptureBodies
			_ = cfg.MaxBodyBytes
			_ = cfg.MaskFields[0]
			_ = cfg.DenyHeaders[0]
			_ = cfg.Masker("f", nil)
			_ = cfg.SkipBodyPaths[0]
			_ = cfg.ExcludePaths[0]
			_ = cfg.PathNormalizer("/X")
		}()
	}
	wg.Wait()
}
