// defaults.go — the mail room's house rules: NewConfig and the shared mask
// defaults every provider Config starts from.

package wirelog

// defaultMaxBodyBytes caps captured body bytes unless overridden.
const defaultMaxBodyBytes = 16384

// defaultMaskFields is the one shared mask list; extend via WithExtraMaskFields.
var defaultMaskFields = []string{
	"msisdn", "phone", "phone_number", "mobile", "account_number",
	"account_name", "iban", "bvn", "nin", "pin", "otp", "cvv", "pan",
	"password", "secret", "token", "access_token", "refresh_token",
	"api_key", "first_name", "last_name", "address", "email",
	"receiver_account", "receiver_account_number", "sender_phone_number",
}

var defaultSkipBodyPaths = []string{"/oauth", "/token", "/auth"}

var defaultExcludePaths = []string{"/health", "/ping", "/status"}

// NewConfig mints a provider Config with the shared defaults applied.
// CaptureBodies never defaults true. Slices are copied so configs never
// share backing arrays with the defaults or each other.
func NewConfig(provider string, opts ...ConfigOption) Config {
	cfg := Config{
		Provider:       provider,
		MaxBodyBytes:   defaultMaxBodyBytes,
		MaskFields:     append([]string(nil), defaultMaskFields...),
		SkipBodyPaths:  append([]string(nil), defaultSkipBodyPaths...),
		ExcludePaths:   append([]string(nil), defaultExcludePaths...),
		PathNormalizer: DefaultNormalizer,
	}
	for _, opt := range opts {
		opt(&cfg)
	}
	return cfg
}

// WithExtraMaskFields APPENDS to the shared mask list, never replaces it.
func WithExtraMaskFields(f ...string) ConfigOption {
	return func(c *Config) { c.MaskFields = append(c.MaskFields, f...) }
}

// WithCaptureBodies toggles body capture.
func WithCaptureBodies(b bool) ConfigOption {
	return func(c *Config) { c.CaptureBodies = b }
}

// WithExtraDenyHeaders appends header names always masked, on top of the built-in auth denylist.
func WithExtraDenyHeaders(h ...string) ConfigOption {
	return func(c *Config) { c.DenyHeaders = append(c.DenyHeaders, h...) }
}

// WithExtraExcludePaths appends paths that produce no record at all.
func WithExtraExcludePaths(p ...string) ConfigOption {
	return func(c *Config) { c.ExcludePaths = append(c.ExcludePaths, p...) }
}

// WithExtraSkipBodyPaths appends paths recorded without bodies.
func WithExtraSkipBodyPaths(p ...string) ConfigOption {
	return func(c *Config) { c.SkipBodyPaths = append(c.SkipBodyPaths, p...) }
}

// WithMasker sets a custom Masker for JSON body fields.
func WithMasker(m Masker) ConfigOption {
	return func(c *Config) { c.Masker = m }
}
