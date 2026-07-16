// config.go — a provider's standing instruction sheet: the per-provider
// capture configuration types, read-only once a transport is minted.

package wirelog

// Operation labels the business action behind a provider call, e.g. "payout.execute".
type Operation string

// Masker transforms the value of a matched JSON body field; field arrives lowercased.
// It applies to JSON body fields only — denied headers always become the mask constant.
type Masker func(field string, value any) any

// Config is per-provider capture configuration. It is read-only shared state
// once HTTPClient mints a transport from it; never mutate it afterwards (B17).
type Config struct {
	Provider       string
	Consumer       string
	CaptureBodies  bool
	MaxBodyBytes   int // default 16384
	MaskFields     []string
	DenyHeaders    []string
	Masker         Masker
	SkipBodyPaths  []string            // substring match on req.URL.Path only: metadata+sizes, never bodies
	ExcludePaths   []string            // substring match on req.URL.Path only: no record at all
	PathNormalizer func(string) string // default DefaultNormalizer
}

// ConfigOption customises a Config minted by NewConfig.
type ConfigOption func(*Config)
