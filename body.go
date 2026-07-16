// body.go — the photocopier: request snapshots via GetBody and the eager
// response-body copy that hands the caller back identical bytes.

package wirelog

import (
	"bytes"
	"io"
	"net/http"
)

// snapshotRequestBody captures the request body via req.GetBody ONLY (B3);
// req.Body is never consumed. Returns nil when no snapshot is possible, in
// which case sizes fall back to req.ContentLength.
func snapshotRequestBody(req *http.Request) []byte {
	if req.Body == nil || req.GetBody == nil {
		return nil
	}
	rc, err := req.GetBody()
	if err != nil {
		return nil
	}
	b, err := io.ReadAll(rc)
	_ = rc.Close()
	if err != nil {
		return nil
	}
	return b
}

// copyBody eagerly reads rc to EOF and closes it (B3). It returns every byte
// read plus any read error; both are replayed to the caller via swapBody.
func copyBody(rc io.ReadCloser) ([]byte, error) {
	b, err := io.ReadAll(rc)
	_ = rc.Close()
	return b, err
}

// swapBody replaces resp.Body with a reader yielding the already-read bytes
// and then the original read error — the caller opens the same letter while
// the mail room keeps the photocopy.
func swapBody(resp *http.Response, full []byte, readErr error) {
	if readErr == nil {
		resp.Body = io.NopCloser(bytes.NewReader(full))
		return
	}
	resp.Body = &replayBody{r: bytes.NewReader(full), err: readErr}
}

// replayBody yields buffered bytes, then the original read error in place of EOF.
type replayBody struct {
	r   *bytes.Reader
	err error
}

func (b *replayBody) Read(p []byte) (int, error) {
	n, err := b.r.Read(p)
	if err == io.EOF {
		return n, b.err
	}
	return n, err
}

func (b *replayBody) Close() error { return nil }
