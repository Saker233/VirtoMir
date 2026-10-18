package api

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/onurbaydas/virtomir/internal/cluster"
	"github.com/onurbaydas/virtomir/internal/drs"
	"github.com/onurbaydas/virtomir/internal/live"
	"github.com/onurbaydas/virtomir/internal/virt"
)

// drsRoutes registers DRS endpoints and unified cluster inventory routes.
func (s *Server) drsRoutes(mux *routeMux) {
	mux.HandleFunc("GET /api/v1/cluster/drs", s.getDRS)
	mux.HandleFunc("PUT /api/v1/cluster/drs/config", s.setDRSConfig)
	mux.HandleFunc("POST /api/v1/cluster/drs/rules", s.createDRSRule)
	mux.HandleFunc("DELETE /api/v1/cluster/drs/rules/{id}", s.deleteDRSRule)
	mux.HandleFunc("POST /api/v1/cluster/drs/recommendations/{id}/apply", s.applyDRSRecommendation)

	// Unified vCenter-style cluster inventory: returns all VMs across all cluster nodes
	mux.HandleFunc("GET /api/v1/cluster/inventory/machines", s.getClusterInventoryMachines)
}

// ClusterDomainSummary is a domain with its physical host information for the unified view.
type ClusterDomainSummary struct {
	virt.Domain
	HostID   string `json:"hostId"`
	HostName string `json:"hostName"`
}

func (s *Server) getDRS(w http.ResponseWriter, r *http.Request) {
	if s.drs == nil {
		s.writeJSON(w, http.StatusOK, drs.Status{Enabled: false, BalanceScore: 100.0})
		return
	}

	hosts, domains := s.collectClusterPlacements(r.Context())
	status := s.drs.Evaluate(r.Context(), hosts, domains)
	s.writeJSON(w, http.StatusOK, status)
}

func (s *Server) setDRSConfig(w http.ResponseWriter, r *http.Request) {
	if s.drs == nil {
		s.writeJSON(w, http.StatusNotFound, map[string]string{"error": "DRS is not available on this host"})
		return
	}
	var req drs.Config
	if !s.decode(w, r, &req) {
		return
	}
	if err := s.drs.SetConfig(req); err != nil {
		s.writeError(w, err)
		return
	}
	s.writeJSON(w, http.StatusOK, s.drs.GetConfig())
}

func (s *Server) createDRSRule(w http.ResponseWriter, r *http.Request) {
	if s.drs == nil {
		s.writeJSON(w, http.StatusNotFound, map[string]string{"error": "DRS is not available"})
		return
	}
	var req drs.AffinityRule
	if !s.decode(w, r, &req) {
		return
	}
	created, err := s.drs.AddRule(req)
	if err != nil {
		s.writeError(w, err)
		return
	}
	s.writeJSON(w, http.StatusCreated, created)
}

func (s *Server) deleteDRSRule(w http.ResponseWriter, r *http.Request) {
	if s.drs == nil {
		s.writeJSON(w, http.StatusNotFound, map[string]string{"error": "DRS is not available"})
		return
	}
	id := r.PathValue("id")
	if err := s.drs.DeleteRule(id); err != nil {
		s.writeError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// applyDRSRecommendation carries a recommendation out: the machine's host live-migrates it
// to the target through the same path as the Move button (checks, job, activity).
func (s *Server) applyDRSRecommendation(w http.ResponseWriter, r *http.Request) {
	if s.drs == nil {
		s.writeJSON(w, http.StatusNotFound, map[string]string{"error": "DRS is not available"})
		return
	}
	if s.noCluster(w) {
		return
	}

	hosts, domains := s.collectClusterPlacements(r.Context())
	status := s.drs.Evaluate(r.Context(), hosts, domains)

	recID := r.PathValue("id")
	var rec *drs.Recommendation
	for i := range status.Recommendations {
		if status.Recommendations[i].ID == recID {
			rec = &status.Recommendations[i]
			break
		}
	}
	if rec == nil {
		s.writeJSON(w, http.StatusNotFound, map[string]string{"error": "recommendation not found or already applied"})
		return
	}
	s.log.Info("applying DRS recommendation", "vm", rec.DomainName, "from", rec.SourceName, "to", rec.TargetName)

	movePath := "/api/v1/domains/" + rec.DomainUUID + "/move"
	body := map[string]string{"host": rec.TargetHostID}
	if rec.SourceHostID == s.cluster.SelfID() {
		raw, _ := json.Marshal(body)
		inner := r.Clone(r.Context())
		inner.URL.Path, inner.URL.RawPath = movePath, ""
		inner.Body, inner.ContentLength = io.NopCloser(bytes.NewReader(raw)), int64(len(raw))
		inner.SetPathValue("uuid", rec.DomainUUID)
		s.moveDomain(w, inner)
		return
	}
	src, ok := s.cluster.Member(rec.SourceHostID)
	if !ok {
		s.writeJSON(w, http.StatusConflict, map[string]string{"error": "the machine's host is no longer a cluster member"})
		return
	}
	id := identity(r)
	var job json.RawMessage
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	if err := s.peer(src).As(id.Name, id.Role.String(), id.Token).Call(ctx, http.MethodPost, movePath, body, &job); err != nil {
		s.writeJSON(w, http.StatusBadGateway, map[string]string{"error": src.Name + ": " + err.Error()})
		return
	}
	// The job runs on the machine's host; the UI follows it at /api/v1/hosts/<id>/jobs/<job>.
	s.writeJSON(w, http.StatusAccepted, map[string]any{
		"message":        "Live migration of " + rec.DomainName + " to " + rec.TargetName + " started on " + src.Name,
		"recommendation": rec,
		"job":            job,
		"jobHost":        src.ID,
	})
}

// getClusterInventoryMachines returns all machines across all nodes in the cluster.
func (s *Server) getClusterInventoryMachines(w http.ResponseWriter, r *http.Request) {
	selfID := ""
	if s.cluster != nil {
		selfID = s.cluster.SelfID()
	}

	localVMs, err := s.backend.ListDomains()
	if err != nil {
		s.writeError(w, err)
		return
	}

	out := make([]ClusterDomainSummary, 0, len(localVMs))
	for _, vm := range localVMs {
		out = append(out, ClusterDomainSummary{Domain: vm, HostID: selfID, HostName: s.hostName})
	}

	if s.cluster != nil && s.cluster.Clustered() {
		members, _ := s.cluster.Members()
		var mu sync.Mutex
		var wg sync.WaitGroup
		for _, node := range members {
			if node.ID == selfID {
				continue
			}
			wg.Add(1)
			go func(n cluster.Node) {
				defer wg.Done()
				ctx, cancel := context.WithTimeout(r.Context(), 4*time.Second)
				defer cancel()
				var remote struct {
					Domains []virt.Domain `json:"domains"`
				}
				if err := s.peer(n).Call(ctx, http.MethodGet, "/api/v1/domains", nil, &remote); err != nil {
					return
				}
				mu.Lock()
				for _, rvm := range remote.Domains {
					out = append(out, ClusterDomainSummary{Domain: rvm, HostID: n.ID, HostName: n.Name})
				}
				mu.Unlock()
			}(node)
		}
		wg.Wait()
	}

	s.writeJSON(w, http.StatusOK, out)
}

// placements turns one host's live snapshot into DRS input.
func placements(id, name string, snap live.Snapshot) (drs.HostMetrics, []drs.DomainPlacement) {
	h := drs.HostMetrics{
		ID: id, Name: name, Reachable: true,
		CPUPct:    snap.Host.CPUPercent,
		MemUsedB:  snap.Host.MemUsedKiB << 10,
		MemTotalB: snap.Host.MemTotalKiB << 10,
		VMCount:   len(snap.Domains),
	}
	if snap.Host.MemTotalKiB > 0 {
		h.MemPct = float64(snap.Host.MemUsedKiB) * 100 / float64(snap.Host.MemTotalKiB)
	}
	doms := make([]drs.DomainPlacement, 0, len(snap.Domains))
	for _, d := range snap.Domains {
		doms = append(doms, drs.DomainPlacement{
			UUID: d.UUID, Name: d.Name, HostID: id,
			State:    strings.ToLower(string(d.State)),
			CPUPct:   d.CPUPercent,
			MemUsedB: d.MemoryKiB << 10,
		})
	}
	return h, doms
}

func (s *Server) collectClusterPlacements(ctx context.Context) ([]drs.HostMetrics, []drs.DomainPlacement) {
	selfID := ""
	if s.cluster != nil {
		selfID = s.cluster.SelfID()
	}

	var local live.Snapshot
	backlog, _, unsubscribe := s.live.Subscribe()
	unsubscribe()
	if len(backlog) > 0 {
		_ = json.Unmarshal(backlog[len(backlog)-1], &local)
	}
	self, domains := placements(selfID, s.hostName, local)
	hosts := []drs.HostMetrics{self}

	if s.cluster != nil && s.cluster.Clustered() {
		members, _ := s.cluster.Members()
		var mu sync.Mutex
		var wg sync.WaitGroup
		for _, node := range members {
			if node.ID == selfID {
				continue
			}
			wg.Add(1)
			go func(n cluster.Node) {
				defer wg.Done()
				c, cancel := context.WithTimeout(ctx, 3*time.Second)
				defer cancel()
				var snap live.Snapshot
				err := s.peer(n).Call(c, http.MethodGet, "/api/v1/summary", nil, &snap)
				mu.Lock()
				defer mu.Unlock()
				if err != nil {
					hosts = append(hosts, drs.HostMetrics{ID: n.ID, Name: n.Name})
					return
				}
				h, d := placements(n.ID, n.Name, snap)
				hosts = append(hosts, h)
				domains = append(domains, d...)
			}(node)
		}
		wg.Wait()
	}
	return hosts, domains
}
