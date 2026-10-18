package api

import (
	"fmt"
	"net/http"
	"slices"

	"github.com/onurbaydas/virtomir/internal/placement"
)

// Required roles need no special configuration: requiredRole in internal/api/access.go
// automatically grants GET to viewers and writes (POST, PUT, DELETE) to admins.

func (s *Server) placementRoutes(mux *routeMux) {
	mux.HandleFunc("GET /api/v1/placement", s.getPlacement)
	mux.HandleFunc("POST /api/v1/placement/rules", s.createPlacementRule)
	mux.HandleFunc("PUT /api/v1/placement/rules/{id}", s.updatePlacementRule)
	mux.HandleFunc("DELETE /api/v1/placement/rules/{id}", s.deletePlacementRule)
	mux.HandleFunc("PUT /api/v1/placement/hosts/{id}/labels", s.setHostLabels)
	mux.HandleFunc("GET /api/v1/placement/violations", s.getPlacementViolations)
	mux.HandleFunc("POST /api/v1/placement/check", s.checkPlacement)
}

func (s *Server) getPlacement(w http.ResponseWriter, r *http.Request) {
	if s.placement == nil {
		s.writeJSON(w, http.StatusNotFound, map[string]string{"error": "placement is not available"})
		return
	}

	rules := s.placement.Rules()
	if rules == nil {
		rules = []placement.Rule{}
	}

	hostLabels := s.placement.Labels()
	if hostLabels == nil {
		hostLabels = make(map[string]map[string]string)
	}

	_, domains := s.collectClusterPlacements(r.Context(), identity(r))
	vms := make([]placement.VMRef, len(domains))
	for i, d := range domains {
		vms[i] = placement.VMRef{
			UUID:    d.UUID,
			Name:    d.Name,
			HostID:  d.HostID,
			Running: d.State == "running",
		}
	}

	violations := placement.Violations(rules, vms)
	if violations == nil {
		violations = []placement.Violation{}
	}

	s.writeJSON(w, http.StatusOK, map[string]any{
		"rules":      rules,
		"hostLabels": hostLabels,
		"violations": violations,
	})
}

func (s *Server) createPlacementRule(w http.ResponseWriter, r *http.Request) {
	if s.placement == nil {
		s.writeJSON(w, http.StatusNotFound, map[string]string{"error": "placement is not available"})
		return
	}

	var req placement.Rule
	if !s.decode(w, r, &req) {
		return
	}

	created, err := s.placement.AddRule(req)
	if err != nil {
		s.writeError(w, err)
		return
	}

	s.replicateToMembers(r, http.MethodPost, "/api/v1/placement/rules", created)
	s.writeJSON(w, http.StatusCreated, created)
}

func (s *Server) updatePlacementRule(w http.ResponseWriter, r *http.Request) {
	if s.placement == nil {
		s.writeJSON(w, http.StatusNotFound, map[string]string{"error": "placement is not available"})
		return
	}

	id := r.PathValue("id")
	rules := s.placement.Rules()
	if !slices.ContainsFunc(rules, func(rule placement.Rule) bool { return rule.ID == id }) {
		s.writeJSON(w, http.StatusNotFound, map[string]string{"error": fmt.Sprintf("rule %q not found", id)})
		return
	}

	var req placement.Rule
	if !s.decode(w, r, &req) {
		return
	}
	req.ID = id

	saved, err := s.placement.AddRule(req)
	if err != nil {
		s.writeError(w, err)
		return
	}

	s.replicateToMembers(r, http.MethodPut, "/api/v1/placement/rules/"+id, saved)
	s.writeJSON(w, http.StatusOK, saved)
}

func (s *Server) deletePlacementRule(w http.ResponseWriter, r *http.Request) {
	if s.placement == nil {
		s.writeJSON(w, http.StatusNotFound, map[string]string{"error": "placement is not available"})
		return
	}

	id := r.PathValue("id")
	if err := s.placement.DeleteRule(id); err != nil {
		s.writeError(w, err)
		return
	}

	s.replicateToMembers(r, http.MethodDelete, "/api/v1/placement/rules/"+id, nil)
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) setHostLabels(w http.ResponseWriter, r *http.Request) {
	if s.placement == nil {
		s.writeJSON(w, http.StatusNotFound, map[string]string{"error": "placement is not available"})
		return
	}

	id := r.PathValue("id")
	if !s.isKnownHost(id) {
		s.writeJSON(w, http.StatusNotFound, map[string]string{"error": fmt.Sprintf("host %q not found", id)})
		return
	}

	var req struct {
		Labels map[string]string `json:"labels"`
	}
	if !s.decode(w, r, &req) {
		return
	}

	if err := s.placement.SetLabels(id, req.Labels); err != nil {
		s.writeError(w, err)
		return
	}

	s.replicateToMembers(r, http.MethodPut, "/api/v1/placement/hosts/"+id+"/labels", req)
	labels := s.placement.Labels()[id]
	if labels == nil {
		labels = map[string]string{}
	}
	s.writeJSON(w, http.StatusOK, map[string]any{"labels": labels})
}

func (s *Server) getPlacementViolations(w http.ResponseWriter, r *http.Request) {
	if s.placement == nil {
		s.writeJSON(w, http.StatusNotFound, map[string]string{"error": "placement is not available"})
		return
	}

	rules := s.placement.Rules()
	_, domains := s.collectClusterPlacements(r.Context(), identity(r))
	vms := make([]placement.VMRef, len(domains))
	for i, d := range domains {
		vms[i] = placement.VMRef{
			UUID:    d.UUID,
			Name:    d.Name,
			HostID:  d.HostID,
			Running: d.State == "running",
		}
	}

	violations := placement.Violations(rules, vms)
	if violations == nil {
		violations = []placement.Violation{}
	}

	s.writeJSON(w, http.StatusOK, violations)
}

func (s *Server) checkPlacement(w http.ResponseWriter, r *http.Request) {
	if s.placement == nil {
		s.writeJSON(w, http.StatusNotFound, map[string]string{"error": "placement is not available"})
		return
	}

	var req struct {
		DomainUUID string `json:"domainUuid"`
		HostID     string `json:"hostId"`
	}
	if !s.decode(w, r, &req) {
		return
	}

	rules := s.placement.Rules()
	_, domains := s.collectClusterPlacements(r.Context(), identity(r))
	vms := make([]placement.VMRef, len(domains))
	for i, d := range domains {
		vms[i] = placement.VMRef{
			UUID:    d.UUID,
			Name:    d.Name,
			HostID:  d.HostID,
			Running: d.State == "running",
		}
	}

	hard, soft := placement.Check(rules, vms, req.DomainUUID, req.HostID)
	if hard == nil {
		hard = []string{}
	}
	if soft == nil {
		soft = []string{}
	}

	s.writeJSON(w, http.StatusOK, map[string]any{
		"ok":   len(hard) == 0,
		"hard": hard,
		"soft": soft,
	})
}

func (s *Server) isKnownHost(hostID string) bool {
	if hostID == "" {
		return false
	}
	if s.cluster != nil {
		if hostID == s.cluster.SelfID() {
			return true
		}
		if _, ok := s.cluster.Member(hostID); ok {
			return true
		}
	}
	if s.hostName != "" && hostID == s.hostName {
		return true
	}
	if s.cluster == nil || !s.cluster.Clustered() {
		if hostID == "local" || hostID == "self" {
			return true
		}
	}
	return false
}
