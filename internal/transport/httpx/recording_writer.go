package httpx

import "net/http"

// recordingWriter wraps an http.ResponseWriter to track whether the response has
// been committed (headers/body successfully sent). The recoverer uses committed
// to decide whether a post-panic 500 is safe.
//
// It does NOT declare optional interfaces (Flusher/Hijacker/Pusher/...)
// unconditionally — that would falsely advertise capabilities the underlying
// writer lacks. Instead it exposes Unwrap() so callers use http.ResponseController
// (Go 1.20+), which discovers Flush/Hijack/SetDeadline on the real writer and
// returns ErrNotSupported otherwise.
//
// committed is set only AFTER a successful underlying WriteHeader/Write, so a
// panic inside the underlying call (before the response is actually on the wire)
// still counts as uncommitted and yields a controlled 500.
type recordingWriter struct {
	http.ResponseWriter
	committed bool
}

// Unwrap exposes the underlying writer for http.ResponseController so Flush,
// Hijack, and deadline controls reach the real writer without this wrapper
// pretending to implement them.
func (w *recordingWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }

// WriteHeader has no return value; the header is committed once the underlying
// call returns, so mark committed only afterwards.
func (w *recordingWriter) WriteHeader(status int) {
	w.ResponseWriter.WriteHeader(status)
	w.committed = true
}

// Write marks committed after the underlying write returns. Any n>0 means bytes
// reached the writer (even with a non-nil err), so the response is in flight and
// must not be overwritten by a recovery 500.
func (w *recordingWriter) Write(b []byte) (int, error) {
	n, err := w.ResponseWriter.Write(b)
	if err == nil || n > 0 {
		w.committed = true
	}
	return n, err
}

// FlushError lets ResponseController flush through this wrapper while keeping
// commit tracking accurate. It does not make recordingWriter an http.Flusher.
func (w *recordingWriter) FlushError() error {
	err := http.NewResponseController(w.ResponseWriter).Flush()
	if err == nil {
		w.committed = true
	}
	return err
}
