// logger.go — the incident book: the minimal logging contract wirelog writes
// its one line per failed delivery (batch insert) to.

package wirelog

// Logger is the minimal logger contract — any logger adapts in one line.
type Logger interface {
	Printf(format string, args ...any)
}

// nopLogger is the default: silent (B2).
type nopLogger struct{}

// Printf discards the line; the default logger never emits anything.
func (nopLogger) Printf(string, ...any) {}
