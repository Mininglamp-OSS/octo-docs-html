package httpx_test

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Mininglamp-OSS/octo-docs-html/internal/config"
	"github.com/Mininglamp-OSS/octo-docs-html/internal/platform/log"
	"github.com/Mininglamp-OSS/octo-docs-html/internal/service"
	"github.com/Mininglamp-OSS/octo-docs-html/internal/storage/memory"
	"github.com/Mininglamp-OSS/octo-docs-html/internal/transport/httpx"
)

// buildRecoverTestServer returns a minimal Server for exercising the recoverer.
func buildRecoverTestServer(t *testing.T) *httpx.Server {
	t.Helper()
	cfg := &config.Config{
		WriteToken: "test-token", MaxHTMLBytes: 5 << 20, RepoURL: "https://x",
		MaxAssetBytes: 25 << 20, AssetMIMEAllow: []string{"image/png"},
	}
	store := memory.New()
	return httpx.New(httpx.Deps{
		Config: cfg, Logger: log.New("silent"),
		Docs:     service.NewDocService(store, store, service.NewCommentService(store, nil), nil, cfg.BaseURL, cfg.MaxHTMLBytes),
		Comments: service.NewCommentService(store, nil),
		Assets:   service.NewAssetService(store, store, nil, cfg.MaxAssetBytes, cfg.AssetMIMEAllow),
		Auth:     service.NewAuthService(store, cfg, nil),
	})
}

// P2: the outermost recoverer catches a panic before any write and emits a
// controlled 500.
func TestRecovererPanicBeforeWriteEmits500(t *testing.T) {
	srv := buildRecoverTestServer(t)
	h := srv.RecovererForTest(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		panic("boom before write")
	}))
	rec := httptest.NewRecorder()
	func() {
		defer func() {
			if r := recover(); r != nil {
				t.Fatalf("panic escaped recoverer: %v", r)
			}
		}()
		h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/panic", nil))
	}()
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("panic-before-write status = %d; want 500", rec.Code)
	}
}

// P2: a handler that commits the response (WriteHeader+body) then panics must
// keep its original response — the recoverer must NOT append a second
// status/body.
func TestRecovererPanicAfterCommitPreservesResponse(t *testing.T) {
	srv := buildRecoverTestServer(t)
	h := srv.RecovererForTest(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"data":"partial"}`))
		panic("boom after commit")
	}))
	rec := httptest.NewRecorder()
	func() {
		defer func() {
			if r := recover(); r != nil {
				t.Fatalf("panic escaped recoverer: %v", r)
			}
		}()
		h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/panic", nil))
	}()
	if rec.Code != http.StatusCreated {
		t.Fatalf("committed status overwritten = %d; want 201 preserved", rec.Code)
	}
	if body := rec.Body.String(); body != `{"data":"partial"}` {
		t.Fatalf("committed body corrupted = %q; want original preserved", body)
	}
}

// P2: a panic in a middleware wrapped INSIDE the recoverer is also caught.
func TestRecovererCatchesMiddlewarePanic(t *testing.T) {
	srv := buildRecoverTestServer(t)
	mw := func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
			panic("boom in middleware")
		})
	}
	h := srv.RecovererForTest(mw(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {})))
	rec := httptest.NewRecorder()
	func() {
		defer func() {
			if r := recover(); r != nil {
				t.Fatalf("middleware panic escaped recoverer: %v", r)
			}
		}()
		h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/mw", nil))
	}()
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("middleware panic status = %d; want 500", rec.Code)
	}
}
