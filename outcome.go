// outcome.go — the delivery stamp: classifies each call's result as
// success, provider_error, timeout or network.

package wirelog

import (
	"context"
	"errors"
	"net"
	"net/http"
)

// outcome values stored in the outcome column (B7).
const (
	outcomeSuccess       = "success"
	outcomeProviderError = "provider_error"
	outcomeTimeout       = "timeout"
	outcomeNetwork       = "network"
)

// classify maps a transport result to an outcome (B7). errors.Is/As unwrap,
// so DeadlineExceeded inside a *url.Error still classifies as timeout.
func classify(resp *http.Response, err error) string {
	if err != nil {
		if errors.Is(err, context.DeadlineExceeded) {
			return outcomeTimeout
		}
		var ne net.Error
		if errors.As(err, &ne) && ne.Timeout() {
			return outcomeTimeout
		}
		return outcomeNetwork
	}
	if resp != nil && resp.StatusCode >= 200 && resp.StatusCode < 300 {
		return outcomeSuccess
	}
	return outcomeProviderError
}
