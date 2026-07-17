package wirelog

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/url"
	"os"
	"syscall"
	"testing"
	"time"
)

// timeoutNetErr is a minimal net.Error with Timeout() true and no
// context.DeadlineExceeded in its chain.
type timeoutNetErr struct{}

// Error returns the fake's fixed message.
func (timeoutNetErr) Error() string { return "i/o timeout" }

// Timeout marks the fake as a timeout so classify must map it to "timeout".
func (timeoutNetErr) Timeout() bool { return true }

// Temporary satisfies net.Error; the value is irrelevant to classification.
func (timeoutNetErr) Temporary() bool { return false }

// TestClassify tables every B7 mapping: 2xx, provider errors, wrapped and
// naked timeouts, and network failures.
func TestClassify(t *testing.T) {
	connRefused := &net.OpError{
		Op:  "dial",
		Net: "tcp",
		Err: os.NewSyscallError("connect", syscall.ECONNREFUSED),
	}
	tests := []struct {
		name string
		resp *http.Response
		err  error
		want string
	}{
		{"200", &http.Response{StatusCode: 200}, nil, outcomeSuccess},
		{"201", &http.Response{StatusCode: 201}, nil, outcomeSuccess},
		{"404", &http.Response{StatusCode: 404}, nil, outcomeProviderError},
		{"500", &http.Response{StatusCode: 500}, nil, outcomeProviderError},
		{"status 0", &http.Response{StatusCode: 0}, nil, outcomeProviderError},
		{"naked DeadlineExceeded", nil, context.DeadlineExceeded, outcomeTimeout},
		{"DeadlineExceeded in url.Error", nil, &url.Error{Op: "Post", URL: "http://x", Err: context.DeadlineExceeded}, outcomeTimeout},
		{"net.Error timeout", nil, timeoutNetErr{}, outcomeTimeout},
		{"wrapped net.Error timeout", nil, &url.Error{Op: "Get", URL: "http://x", Err: timeoutNetErr{}}, outcomeTimeout},
		{"connection refused", nil, connRefused, outcomeNetwork},
		{"wrapped connection refused", nil, &url.Error{Op: "Get", URL: "http://x", Err: connRefused}, outcomeNetwork},
		{"plain error", nil, errors.New("boom"), outcomeNetwork},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := classify(tt.resp, tt.err); got != tt.want {
				t.Fatalf("classify(%v, %v) = %q, want %q", tt.resp, tt.err, got, tt.want)
			}
		})
	}
}

// TestClassifyRealDialTimeout confirms a real net dial timeout classifies as
// timeout end-to-end, not just via the fake.
func TestClassifyRealDialTimeout(t *testing.T) {
	// 203.0.113.0/24 is TEST-NET-3, unroutable: the dial can only time out.
	_, err := net.DialTimeout("tcp", "203.0.113.1:81", 5*time.Millisecond)
	if err == nil {
		t.Skip("dial unexpectedly succeeded")
	}
	if got := classify(nil, err); got != outcomeTimeout {
		t.Fatalf("classify(real dial timeout %v) = %q, want %q", err, got, outcomeTimeout)
	}
}
