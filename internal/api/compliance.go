package api

import (
	"net/http"
)

func (s *Server) complianceRoutes(mux *routeMux) {
	mux.HandleFunc("GET /api/v1/compliance/fips", s.getFIPS)
}

func (s *Server) getFIPS(w http.ResponseWriter, _ *http.Request) {
	s.writeJSON(w, http.StatusOK, s.FIPSInfo())
}
