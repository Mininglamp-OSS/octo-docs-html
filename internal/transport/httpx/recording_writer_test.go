package httpx

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
)

// plainWriter implements only http.ResponseWriter (no Flush/Hijack), to prove
// the wrapper does not falsely advertise optional interfaces via Unwrap.
type plainWriter struct {
	header http.Header
	status int
	body   []byte
}

func (p *plainWriter) Header() http.Header {
	if p.header == nil {
		p.header = http.Header{}
	}
	return p.header
}
func (p *plainWriter) WriteHeader(s int)           { p.status = s }
func (p *plainWriter) Write(b []byte) (int, error) { p.body = append(p.body, b...); return len(b), nil }

// flushWriter adds Flush so ResponseController.Flush() succeeds.
type flushWriter struct {
	plainWriter
	flushed bool
}

func (f *flushWriter) Flush() { f.flushed = true }

// A recordingWriter over a plain (non-flushable) writer must NOT satisfy
// http.Flusher itself, and ResponseController.Flush must report ErrNotSupported.
func TestRecordingWriterDoesNotFalselyAdvertiseInterfaces(t *testing.T) {
	rw := &recordingWriter{ResponseWriter: &plainWriter{}}
	if _, ok := any(rw).(http.Flusher); ok {
		t.Fatalf("recordingWriter must not advertise http.Flusher unconditionally")
	}
	if _, ok := any(rw).(http.Hijacker); ok {
		t.Fatalf("recordingWriter must not advertise http.Hijacker unconditionally")
	}
	if _, ok := any(rw).(http.Pusher); ok {
		t.Fatalf("recordingWriter must not advertise http.Pusher unconditionally")
	}
	if err := http.NewResponseController(rw).Flush(); !errors.Is(err, http.ErrNotSupported) {
		t.Fatalf("Flush on non-flushable underlying = %v; want ErrNotSupported", err)
	}
}

// When the underlying writer supports Flush, ResponseController reaches it
// through Unwrap.
func TestRecordingWriterResponseControllerReachesSupportedWriter(t *testing.T) {
	fw := &flushWriter{}
	rw := &recordingWriter{ResponseWriter: fw}
	if err := http.NewResponseController(rw).Flush(); err != nil {
		t.Fatalf("Flush on flushable underlying = %v; want nil", err)
	}
	if !fw.flushed {
		t.Fatalf("Flush did not reach the underlying writer via Unwrap")
	}
	if !rw.committed {
		t.Fatalf("successful Flush must mark the response committed")
	}
}

// committed flips only after a successful underlying WriteHeader/Write.
func TestRecordingWriterCommittedTracking(t *testing.T) {
	rw := &recordingWriter{ResponseWriter: httptest.NewRecorder()}
	if rw.committed {
		t.Fatalf("committed should start false")
	}
	rw.WriteHeader(http.StatusTeapot)
	if !rw.committed {
		t.Fatalf("committed should be true after WriteHeader")
	}
	rw2 := &recordingWriter{ResponseWriter: httptest.NewRecorder()}
	if _, err := rw2.Write([]byte("x")); err != nil {
		t.Fatalf("write: %v", err)
	}
	if !rw2.committed {
		t.Fatalf("committed should be true after a successful Write")
	}
}

// panicMidWrite panics before committing any bytes. The wrapper must leave
// committed=false so recovery can still respond with a controlled 500.
type panicMidWrite struct{ http.ResponseWriter }

func (panicMidWrite) Write([]byte) (int, error) { panic("boom mid-write") }

func TestRecordingWriterUncommittedWhenUnderlyingWritePanics(t *testing.T) {
	rw := &recordingWriter{ResponseWriter: panicMidWrite{httptest.NewRecorder()}}
	func() {
		defer func() { _ = recover() }()
		_, _ = rw.Write([]byte("x"))
	}()
	if rw.committed {
		t.Fatalf("committed must stay false when the underlying Write panicked before committing bytes")
	}
}
