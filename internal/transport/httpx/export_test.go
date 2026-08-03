package httpx

import (
	"log/slog"
	"net/http"

	"github.com/Mininglamp-OSS/octo-docs-html/internal/service"
)

// RecovererForTest exposes the outermost panic-recovery middleware so external
// _test packages can assert response-aware recovery.
func (s *Server) RecovererForTest(next http.Handler) http.Handler { return s.recoverer(next) }

// InjectCapMarkerForTest exposes the overlay marker mapping to external tests.
func InjectCapMarkerForTest(html string, cap service.Capability) string {
	return injectCapMarker(html, cap)
}

// SetLoggerForTest permits nil-logger recovery coverage.
func (s *Server) SetLoggerForTest(logger *slog.Logger) { s.logger = logger }
