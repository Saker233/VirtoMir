package api

import (
	"net/http"
	"slices"
	"strings"

	"github.com/onurbaydas/virtomir/internal/auth"
	"github.com/onurbaydas/virtomir/internal/project"
)

// Projects (ADR-0060): multi-tenancy. A machine belongs to at most one project, stored
// next to its owner in the domain's <metadata>; a machine without a project is global and
// behaves exactly as before. Project checks run in addition to (not instead of) user
// quotas and resource pools; global admins pass them all.

// projectUsage adds up the machines of one project on this host.
func (s *Server) projectUsage(projectID string) project.Usage {
	var u project.Usage
	doms, err := s.backend.ListDomains()
	if err != nil {
		return u
	}
	var mine []string
	for _, d := range doms {
		if d.Project == projectID {
			u.Machines++
			u.VCPUs += d.VCPUs
			u.MemoryMiB += int64(d.MaxMemoryKiB >> 10)
			mine = append(mine, d.UUID)
		}
	}
	u.DiskGiB = int64(s.diskGiB(mine))
	return u
}

// projectOf is the project a machine belongs to ("" when global).
func (s *Server) projectOf(uuid string) string {
	doms, err := s.backend.ListDomains()
	if err != nil {
		return ""
	}
	for _, d := range doms {
		if strings.EqualFold(d.UUID, uuid) {
			return d.Project
		}
	}
	return ""
}

// projectOperator reports whether the user may change things in the project: they must be
// a global admin or an operator (or better) in the project itself.
func (s *Server) projectOperator(projectID, user string, role auth.Role) bool {
	if role >= auth.Admin {
		return true
	}
	r, ok := s.projects.RoleFor(projectID, user, s.groupsOf(user))
	return ok && (r == "operator" || r == "admin")
}

// checkProject answers 403/409 and returns false when the caller may not put demand into
// project projectID: they must be a global admin or an operator in the project, the storage
// pool and every network must be on the project's allowed lists, and the project's limits
// must cover the demand on top of what its machines use now. An empty projectID (a global
// machine) or no project store at all checks nothing.
func (s *Server) checkProject(w http.ResponseWriter, user string, role auth.Role, projectID string, demand project.Usage, pool string, networks []string) bool {
	if s.projects == nil || projectID == "" {
		return true
	}
	p, ok := s.projects.Get(projectID)
	if !ok {
		s.writeJSON(w, http.StatusNotFound, map[string]string{"error": "no project " + projectID})
		return false
	}
	if !s.projectOperator(projectID, user, role) {
		s.writeJSON(w, http.StatusForbidden, map[string]string{"error": "you are not an operator in project " + projectID})
		return false
	}
	if pool != "" && !slices.Contains(p.StoragePools, pool) {
		s.writeJSON(w, http.StatusConflict, map[string]string{"error": "storage pool " + pool + " is not allowed in project " + projectID})
		return false
	}
	for _, n := range networks {
		if n != "" && !slices.Contains(p.Networks, n) {
			s.writeJSON(w, http.StatusConflict, map[string]string{"error": "network " + n + " is not allowed in project " + projectID})
			return false
		}
	}
	if err := p.Limits.Exceeded(s.projectUsage(projectID), demand); err != nil {
		s.writeJSON(w, http.StatusConflict, map[string]string{"error": err.Error()})
		return false
	}
	return true
}

// recordProject marks which project a machine belongs to.
func (s *Server) recordProject(uuid, projectID string) {
	if projectID == "" || uuid == "" {
		return
	}
	o, ok := s.backend.(interface {
		SetProject(uuid, projectID string) error
	})
	if !ok {
		return
	}
	if err := o.SetProject(uuid, projectID); err != nil {
		s.log.Warn("record project", "domain", uuid, "err", err)
	}
}
