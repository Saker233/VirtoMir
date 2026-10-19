package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/onurbaydas/virtomir/internal/activity"
	"github.com/onurbaydas/virtomir/internal/auth"
	"github.com/onurbaydas/virtomir/internal/catalog"
	"github.com/onurbaydas/virtomir/internal/cluster"
)

// Catalog (ADR-0065): self-service. Admins publish blueprints; members of a project
// order machines from them; approvers (global admins and project admins) answer the
// pending requests; a background job provisions the machine; leases keep how long a
// machine may live. Showback turns what runs into money per project.

func (s *Server) catalogRoutes(mux *routeMux) {
	mux.HandleFunc("GET /api/v1/catalog/blueprints", s.listCatalogBlueprints)
	mux.HandleFunc("POST /api/v1/catalog/blueprints", s.createCatalogBlueprint)
	mux.HandleFunc("GET /api/v1/catalog/blueprints/{id}", s.getCatalogBlueprint)
	mux.HandleFunc("PUT /api/v1/catalog/blueprints/{id}", s.updateCatalogBlueprint)
	mux.HandleFunc("DELETE /api/v1/catalog/blueprints/{id}", s.deleteCatalogBlueprint)
	mux.HandleFunc("GET /api/v1/catalog/requests", s.listCatalogRequests)
	mux.HandleFunc("POST /api/v1/catalog/requests", s.createCatalogRequest)
	mux.HandleFunc("GET /api/v1/catalog/requests/{id}", s.getCatalogRequest)
	mux.HandleFunc("POST /api/v1/catalog/requests/{id}/approve", s.approveCatalogRequest)
	mux.HandleFunc("POST /api/v1/catalog/requests/{id}/reject", s.rejectCatalogRequest)
	mux.HandleFunc("POST /api/v1/catalog/requests/{id}/extend", s.extendCatalogRequest)
	mux.HandleFunc("DELETE /api/v1/catalog/requests/{id}", s.deleteCatalogRequest)
	mux.HandleFunc("GET /api/v1/catalog/pricing", s.getCatalogPricing)
	mux.HandleFunc("PUT /api/v1/catalog/pricing", s.saveCatalogPricing)
	mux.HandleFunc("GET /api/v1/catalog/showback", s.getCatalogShowback)
}

// noCatalog answers the error every handler shows when the store is not wired up.
func (s *Server) noCatalog(w http.ResponseWriter) bool {
	if s.catalog == nil {
		s.writeJSON(w, http.StatusConflict, map[string]string{"error": "the catalog is not available on this host"})
		return true
	}
	return false
}

// decodeOptional reads a JSON body that may be empty (approve without a comment).
// On a malformed body it has already answered 400.
func (s *Server) decodeOptional(w http.ResponseWriter, r *http.Request, v any) bool {
	raw, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxBody))
	if err != nil {
		s.writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid request body: " + err.Error()})
		return false
	}
	if len(bytes.TrimSpace(raw)) == 0 {
		return true
	}
	if err := json.Unmarshal(raw, v); err != nil {
		s.writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid request body: " + err.Error()})
		return false
	}
	return true
}

// catalogErrStatus maps the catalog package's errors to HTTP.
func catalogErrStatus(err error) int {
	switch {
	case errors.Is(err, catalog.ErrNotFound):
		return http.StatusNotFound
	case errors.Is(err, catalog.ErrInvalid):
		return http.StatusUnprocessableEntity
	case errors.Is(err, catalog.ErrConflict):
		return http.StatusConflict
	case errors.Is(err, catalog.ErrUnauthorized):
		return http.StatusForbidden
	}
	return http.StatusUnprocessableEntity
}

// catalogAudit adds the audit entries the API guard alone cannot say: approve, reject,
// extend and blueprint edits name the operation they did.
func (s *Server) catalogAudit(r *http.Request, action string) {
	if s.activity == nil {
		return
	}
	id := identity(r)
	if err := s.activity.Add(activity.Entry{
		User: id.Name, Token: id.Token, Remote: r.RemoteAddr,
		Method: "CATALOG-" + strings.ToUpper(action),
		Path:   r.URL.Path, Status: http.StatusOK,
	}); err != nil {
		s.log.Warn("catalog audit log", "err", err)
	}
}

// catalogToMembers repeats a catalog change on the other members, so every host of the
// cluster has the same blueprints and pricing (one set per cluster, saved on every
// host, like the projects ADR-0060). ?local=1 marks the copies, which are not passed
// on again. The answer says per member whether the change arrived.
func (s *Server) catalogToMembers(r *http.Request, method, path string, body any) []HostResult {
	if r.URL.Query().Get("local") != "" || s.cluster == nil || !s.cluster.Clustered() {
		return nil
	}
	id := identity(r)
	members, _ := s.cluster.Members()
	results := make([]HostResult, 0, len(members))
	var wg sync.WaitGroup
	for _, n := range members {
		if n.ID == s.cluster.SelfID() {
			continue
		}
		results = append(results, HostResult{Host: n.Name})
		res := &results[len(results)-1]
		wg.Add(1)
		go func(n cluster.Node) {
			defer wg.Done()
			res.OK = true
			ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
			defer cancel()
			if err := s.peer(n).As(id.Name, id.Role.String(), id.Token).Call(ctx, method, path+"?local=1", body, nil); err != nil {
				res.OK, res.Error = false, err.Error()
				s.log.Warn("catalog change not copied to a member", "member", n.Name, "err", err)
			}
		}(n)
	}
	wg.Wait()
	return results
}

// listCatalogBlueprints: everyone with a viewer role may look; ?project=<id> answers
// only the blueprints that project may order.
func (s *Server) listCatalogBlueprints(w http.ResponseWriter, r *http.Request) {
	if s.noCatalog(w) {
		return
	}
	bps := s.catalog.ListBlueprints(r.URL.Query().Get("project"))
	s.writeJSON(w, http.StatusOK, map[string]any{"blueprints": bps})
}

func (s *Server) getCatalogBlueprint(w http.ResponseWriter, r *http.Request) {
	if s.noCatalog(w) {
		return
	}
	bp, ok := s.catalog.GetBlueprint(r.PathValue("id"))
	if !ok {
		s.writeJSON(w, http.StatusNotFound, map[string]string{"error": "no blueprint " + r.PathValue("id")})
		return
	}
	s.writeJSON(w, http.StatusOK, bp)
}

func (s *Server) createCatalogBlueprint(w http.ResponseWriter, r *http.Request) {
	if s.noCatalog(w) {
		return
	}
	var bp catalog.Blueprint
	if !s.decode(w, r, &bp) {
		return
	}
	created, err := s.catalog.CreateBlueprint(bp)
	if err != nil {
		s.writeJSON(w, catalogErrStatus(err), map[string]string{"error": err.Error()})
		return
	}
	results := s.catalogToMembers(r, http.MethodPost, "/api/v1/catalog/blueprints", created)
	s.catalogAudit(r, "blueprint-create")
	answer := map[string]any{"blueprint": created}
	if results != nil {
		answer["results"] = results
	}
	s.done(w, http.StatusCreated, answer)
}

func (s *Server) updateCatalogBlueprint(w http.ResponseWriter, r *http.Request) {
	if s.noCatalog(w) {
		return
	}
	var bp catalog.Blueprint
	if !s.decode(w, r, &bp) {
		return
	}
	bp.ID = r.PathValue("id")
	if err := s.catalog.UpdateBlueprint(bp); err != nil {
		s.writeJSON(w, catalogErrStatus(err), map[string]string{"error": err.Error()})
		return
	}
	next, _ := s.catalog.GetBlueprint(bp.ID)
	results := s.catalogToMembers(r, http.MethodPut, "/api/v1/catalog/blueprints/"+bp.ID, next)
	s.catalogAudit(r, "blueprint-update")
	answer := map[string]any{"blueprint": next}
	if results != nil {
		answer["results"] = results
	}
	s.done(w, http.StatusOK, answer)
}

func (s *Server) deleteCatalogBlueprint(w http.ResponseWriter, r *http.Request) {
	if s.noCatalog(w) {
		return
	}
	id := r.PathValue("id")
	if err := s.catalog.DeleteBlueprint(id); err != nil {
		s.writeJSON(w, catalogErrStatus(err), map[string]string{"error": err.Error()})
		return
	}
	results := s.catalogToMembers(r, http.MethodDelete, "/api/v1/catalog/blueprints/"+id, nil)
	s.catalogAudit(r, "blueprint-delete")
	if slices.ContainsFunc(results, func(h HostResult) bool { return !h.OK }) {
		s.done(w, http.StatusOK, map[string]any{"results": results})
		return
	}
	s.done(w, http.StatusNoContent, nil)
}

// noProjectID matches no store record (IDs are slugs); it answers an empty list
// where "not a member of the asked project" must not fall back to everything.
const noProjectID = "‹none›"

// listCatalogRequests: global admins see every request; the others only the ones of
// the projects they belong to. ?project=<id> and ?status=<status> narrow the list.
func (s *Server) listCatalogRequests(w http.ResponseWriter, r *http.Request) {
	if s.noCatalog(w) {
		return
	}
	q := r.URL.Query()
	status := catalog.RequestStatus(q.Get("status"))
	pj := q.Get("project")
	var projectIDs []string
	user := ""
	id := identity(r)
	if id.Role < auth.Admin {
		switch {
		case s.projects == nil:
			user = id.Name // no projects on this host: the caller sees their own orders
		case pj != "":
			if s.memberAtLeast(pj, id, "viewer") {
				projectIDs = []string{pj}
			} else {
				projectIDs = []string{noProjectID}
			}
		default:
			projectIDs = s.catalogProjectsFor(id)
			if len(projectIDs) == 0 {
				projectIDs = []string{noProjectID}
			}
		}
	} else if pj != "" {
		projectIDs = []string{pj}
	}
	s.writeJSON(w, http.StatusOK, map[string]any{"requests": s.catalog.ListRequests(user, projectIDs, status)})
}

// catalogProjectsFor is the list of project IDs the caller belongs to.
func (s *Server) catalogProjectsFor(id auth.Identity) []string {
	if s.projects == nil {
		return nil
	}
	var ids []string
	for _, p := range s.projects.ProjectsFor(id.Name, s.groupsOf(id.Name)) {
		ids = append(ids, p.ID)
	}
	return ids
}

// createCatalogRequest orders a machine from a blueprint into a project; the caller
// must belong to that project (SubmitRequest checks). Without an approver the machine
// goes straight out; with one the request waits in pending_approval.
func (s *Server) createCatalogRequest(w http.ResponseWriter, r *http.Request) {
	if s.noCatalog(w) {
		return
	}
	if s.projects == nil {
		s.writeJSON(w, http.StatusConflict, map[string]string{"error": "projects are not available on this host"})
		return
	}
	var req catalog.Request
	if !s.decode(w, r, &req) {
		return
	}
	id := identity(r)
	saved, err := catalog.SubmitRequest(s.catalog, s.projects, id.Name, s.groupsOf(id.Name), req)
	if err != nil {
		s.writeJSON(w, catalogErrStatus(err), map[string]string{"error": err.Error()})
		return
	}
	s.catalogAudit(r, "request-create")
	s.done(w, http.StatusCreated, saved)
}

// getCatalogRequest: project members and admins only; anyone else gets the same 404
// as an unknown ID, so they cannot probe which requests exist.
func (s *Server) getCatalogRequest(w http.ResponseWriter, r *http.Request) {
	if s.noCatalog(w) {
		return
	}
	req, ok := s.catalog.GetRequest(r.PathValue("id"))
	if !ok {
		s.writeJSON(w, http.StatusNotFound, map[string]string{"error": "no request " + r.PathValue("id")})
		return
	}
	id := identity(r)
	if id.Role < auth.Admin && req.Requester != id.Name && s.projects != nil {
		if _, member := s.projects.RoleFor(req.ProjectID, id.Name, s.groupsOf(id.Name)); !member {
			s.writeJSON(w, http.StatusNotFound, map[string]string{"error": "no request " + req.ID})
			return
		}
	}
	answer := map[string]any{"request": req}
	if lease, ok := s.catalog.GetLeaseByRequest(req.ID); ok {
		answer["lease"] = lease
	}
	s.writeJSON(w, http.StatusOK, answer)
}

func (s *Server) approveCatalogRequest(w http.ResponseWriter, r *http.Request) {
	if s.noCatalog(w) {
		return
	}
	id := identity(r)
	var b struct {
		Comment string `json:"comment"`
	}
	if !s.decodeOptional(w, r, &b) {
		return
	}
	saved, err := catalog.ApproveRequest(s.catalog, s.projects, r.PathValue("id"), id.Name, id.Role, s.groupsOf(id.Name), b.Comment)
	if err != nil {
		if errors.Is(err, catalog.ErrUnauthorized) {
			s.writeJSON(w, http.StatusForbidden, map[string]string{"error": err.Error()})
			return
		}
		s.writeJSON(w, catalogErrStatus(err), map[string]string{"error": err.Error()})
		return
	}
	s.catalogAudit(r, "request-approve")
	title := "Provision catalog machine " + saved.VMName
	if saved.BlueprintID != "" {
		title = "Provision " + saved.VMName + " from " + saved.BlueprintID
	}
	job := s.jobs.Start("catalog-provision", title, func(ctx context.Context, report func(string)) (string, error) {
		dep, ok := s.backend.(catalog.Deployer)
		if !ok {
			return "", fmt.Errorf("this host cannot provision catalog machines")
		}
		return catalog.Provision(ctx, dep, s.projects, s.catalog, saved, report)
	})
	s.writeJSON(w, http.StatusAccepted, job)
}

func (s *Server) rejectCatalogRequest(w http.ResponseWriter, r *http.Request) {
	if s.noCatalog(w) {
		return
	}
	id := identity(r)
	var b struct {
		Comment string `json:"comment"`
	}
	if !s.decodeOptional(w, r, &b) {
		return
	}
	saved, err := catalog.RejectRequest(s.catalog, s.projects, r.PathValue("id"), id.Name, id.Role, s.groupsOf(id.Name), b.Comment)
	if err != nil {
		if errors.Is(err, catalog.ErrUnauthorized) {
			s.writeJSON(w, http.StatusForbidden, map[string]string{"error": err.Error()})
			return
		}
		s.writeJSON(w, catalogErrStatus(err), map[string]string{"error": err.Error()})
		return
	}
	s.catalogAudit(r, "request-reject")
	s.done(w, http.StatusOK, saved)
}

// extendCatalogRequest lengthens a provisioned machine's lease by the named days;
// the requester (and others, checked in ExtendLease) may do it.
func (s *Server) extendCatalogRequest(w http.ResponseWriter, r *http.Request) {
	if s.noCatalog(w) {
		return
	}
	if s.leaseMgr == nil {
		s.writeJSON(w, http.StatusConflict, map[string]string{"error": "leases are not available on this host"})
		return
	}
	var b struct {
		Days int `json:"days"`
	}
	if !s.decode(w, r, &b) {
		return
	}
	id := identity(r)
	lease, err := s.leaseMgr.ExtendLease(r.PathValue("id"), id.Name, id.Role, s.groupsOf(id.Name), b.Days)
	if err != nil {
		if errors.Is(err, catalog.ErrUnauthorized) {
			s.writeJSON(w, http.StatusForbidden, map[string]string{"error": err.Error()})
			return
		}
		s.writeJSON(w, catalogErrStatus(err), map[string]string{"error": err.Error()})
		return
	}
	s.catalogAudit(r, "lease-extend")
	s.done(w, http.StatusOK, lease)
}

// deleteCatalogRequest cancels a pending order (the requester) or deletes any record
// (an admin).
func (s *Server) deleteCatalogRequest(w http.ResponseWriter, r *http.Request) {
	if s.noCatalog(w) {
		return
	}
	req, ok := s.catalog.GetRequest(r.PathValue("id"))
	if !ok {
		s.writeJSON(w, http.StatusNotFound, map[string]string{"error": "no request " + r.PathValue("id")})
		return
	}
	id := identity(r)
	admin := id.Role >= auth.Admin
	requesterCancel := req.Requester == id.Name && req.Status == catalog.StatusPendingApproval
	if !admin && !requesterCancel {
		s.writeJSON(w, http.StatusForbidden, map[string]string{"error": "only the requester may cancel a pending request; admins may delete any record"})
		return
	}
	if err := s.catalog.DeleteRequest(req.ID); err != nil {
		s.writeJSON(w, catalogErrStatus(err), map[string]string{"error": err.Error()})
		return
	}
	s.catalogAudit(r, "request-delete")
	s.done(w, http.StatusNoContent, nil)
}

func (s *Server) getCatalogPricing(w http.ResponseWriter, _ *http.Request) {
	if s.noCatalog(w) {
		return
	}
	s.writeJSON(w, http.StatusOK, s.catalog.GetPricing())
}

func (s *Server) saveCatalogPricing(w http.ResponseWriter, r *http.Request) {
	if s.noCatalog(w) {
		return
	}
	var cfg catalog.PricingConfig
	if !s.decode(w, r, &cfg) {
		return
	}
	if err := s.catalog.SetPricing(cfg); err != nil {
		s.writeJSON(w, catalogErrStatus(err), map[string]string{"error": err.Error()})
		return
	}
	saved := s.catalog.GetPricing()
	results := s.catalogToMembers(r, http.MethodPut, "/api/v1/catalog/pricing", saved)
	s.catalogAudit(r, "pricing-update")
	answer := map[string]any{"pricing": saved}
	if results != nil {
		answer["results"] = results
	}
	s.done(w, http.StatusOK, answer)
}

// getCatalogShowback prices one month (default: the current one) of what runs;
// ?project narrows to one project, ?format answers json (default), csv or html.
// Non-admins see only the projects they belong to.
func (s *Server) getCatalogShowback(w http.ResponseWriter, r *http.Request) {
	if s.noCatalog(w) {
		return
	}
	q := r.URL.Query()
	now := time.Now().UTC()
	year, month := now.Year(), now.Month()
	if v := q.Get("year"); v != "" {
		y, err := strconv.Atoi(v)
		if err != nil {
			s.writeJSON(w, http.StatusBadRequest, map[string]string{"error": "year must be a number"})
			return
		}
		year = y
	}
	if v := q.Get("month"); v != "" {
		m, err := strconv.Atoi(v)
		if err != nil || m < 1 || m > 12 {
			s.writeJSON(w, http.StatusBadRequest, map[string]string{"error": "month must be 1..12"})
			return
		}
		month = time.Month(m)
	}

	id := identity(r)
	if id.Role < auth.Admin && s.projects != nil {
		pj := q.Get("project")
		if pj == "" {
			// Their projects come out one report each, merged into one answer.
			var full catalog.ShowbackReport
			pricing := s.catalog.GetPricing()
			for i, pid := range s.catalogProjectsFor(id) {
				rep, err := catalog.CalculateShowback(s.backend, s.projects, pricing, year, month, pid)
				if err != nil {
					continue
				}
				if i == 0 {
					full = rep
					continue
				}
				full.Projects = append(full.Projects, rep.Projects...)
				full.TotalCost += rep.TotalCost
				full.TotalMachines += rep.TotalMachines
			}
			sort.Slice(full.Projects, func(i, j int) bool {
				if full.Projects[i].ProjectName == full.Projects[j].ProjectName {
					return full.Projects[i].ProjectID < full.Projects[j].ProjectID
				}
				return full.Projects[i].ProjectName < full.Projects[j].ProjectName
			})
			s.writeShowback(w, r, full)
			return
		}
		if !s.memberAtLeast(pj, id, "viewer") {
			s.writeJSON(w, http.StatusForbidden, map[string]string{"error": "you are not a member of project " + pj})
			return
		}
		q.Set("project", pj)
	}
	rep, err := catalog.CalculateShowback(s.backend, s.projects, s.catalog.GetPricing(), year, month, q.Get("project"))
	if err != nil {
		s.writeError(w, err)
		return
	}
	s.writeShowback(w, r, rep)
}

// writeShowback answers the report in the asked format; the JSON one is the default.
func (s *Server) writeShowback(w http.ResponseWriter, r *http.Request, rep catalog.ShowbackReport) {
	format := r.URL.Query().Get("format")
	switch {
	case format == "" || format == "json":
		b, err := catalog.RenderJSON(rep)
		if err != nil {
			s.writeError(w, err)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(b)
	case format == "csv":
		b, err := catalog.RenderCSV(rep)
		if err != nil {
			s.writeError(w, err)
			return
		}
		w.Header().Set("Content-Type", "text/csv; charset=utf-8")
		w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=showback-%s.csv", rep.Period))
		_, _ = w.Write(b)
	case format == "html":
		b, err := catalog.RenderHTML(rep)
		if err != nil {
			s.writeError(w, err)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write(b)
	default:
		s.writeJSON(w, http.StatusBadRequest, map[string]string{"error": "format must be json, csv or html"})
	}
}
