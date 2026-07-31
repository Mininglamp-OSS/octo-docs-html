package httpx

import "net/http"

// RecovererForTest exposes the outermost panic-recovery middleware so external
// _test packages can assert response-aware recovery (uncommitted → 500;
// committed → original response preserved; middleware panic caught).
func (s *Server) RecovererForTest(next http.Handler) http.Handler {
	return s.recoverer(next)
}
