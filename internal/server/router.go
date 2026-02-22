package server

import "net/http"

// NewRouter creates an http.Handler with all routes wired up.
func NewRouter(handler *Handler, auth *Auth, staticFS http.FileSystem) http.Handler {
	mux := http.NewServeMux()

	// Public routes (no auth).
	mux.HandleFunc("GET /healthz", handler.HandleHealthz)
	mux.HandleFunc("GET /readyz", handler.HandleReadyz)
	mux.HandleFunc("/login", handler.HandleLogin)
	mux.HandleFunc("GET /logout", handler.HandleLogout)

	// Static files (no auth).
	mux.Handle("GET /static/", http.StripPrefix("/static/", http.FileServer(staticFS)))

	// Authenticated routes.
	mux.Handle("GET /admin", auth.AuthMiddleware(http.HandlerFunc(handler.HandleAdmin)))
	mux.Handle("GET /admin/log", auth.AuthMiddleware(http.HandlerFunc(handler.HandleLogDetail)))
	mux.Handle("GET /api/logs", auth.AuthMiddleware(http.HandlerFunc(handler.HandleAPILogs)))
	mux.Handle("GET /api/stats", auth.AuthMiddleware(http.HandlerFunc(handler.HandleAPIStats)))
	mux.Handle("GET /api/stats/distribution", auth.AuthMiddleware(http.HandlerFunc(handler.HandleAPIDistribution)))

	return securityHeaders(mux)
}

// securityHeaders wraps a handler with common security response headers.
func securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Frame-Options", "DENY")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Content-Security-Policy", "default-src 'self'; script-src 'self'; style-src 'self' 'unsafe-inline'")
		next.ServeHTTP(w, r)
	})
}
