package api

import "net/http"

// dashboardHandler serves the embedded admin dashboard SPA.
func (s *Server) dashboardHandler(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Write([]byte(dashboardHTML))
}
