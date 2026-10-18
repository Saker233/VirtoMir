package api

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/onurbaydas/virtomir/internal/activity"
	"github.com/onurbaydas/virtomir/internal/auth"
	"github.com/onurbaydas/virtomir/internal/cluster"
	"github.com/onurbaydas/virtomir/internal/drs"
	"github.com/onurbaydas/virtomir/internal/jobs"
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
	mux.HandleFunc("GET /api/v1/cluster/drs/placement", s.drsPlacement)

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
	s.applyRecommendation(r.Context(), w, *rec, identity(r))
}

// applyRecommendation starts rec's live migration as id: the machine's host runs it through
// the same path as the Move button (checks, job, activity), and w gets that answer.
func (s *Server) applyRecommendation(ctx context.Context, w http.ResponseWriter, rec drs.Recommendation, id auth.Identity) {
	s.log.Info("applying DRS recommendation", "vm", rec.DomainName, "from", rec.SourceName, "to", rec.TargetName, "by", id.Name)
	movePath := "/api/v1/domains/" + rec.DomainUUID + "/move"
	body := map[string]string{"host": rec.TargetHostID}
	if rec.SourceHostID == s.cluster.SelfID() {
		raw, _ := json.Marshal(body)
		inner, err := http.NewRequestWithContext(context.WithValue(ctx, identityKey, id), http.MethodPost, movePath, bytes.NewReader(raw))
		if err != nil {
			s.writeError(w, err)
			return
		}
		inner.SetPathValue("uuid", rec.DomainUUID)
		s.moveDomain(w, inner)
		return
	}
	src, ok := s.cluster.Member(rec.SourceHostID)
	if !ok {
		s.writeJSON(w, http.StatusConflict, map[string]string{"error": "the machine's host is no longer a cluster member"})
		return
	}
	var job json.RawMessage
	c, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	if err := s.peer(src).As(id.Name, id.Role.String(), id.Token).Call(c, http.MethodPost, movePath, body, &job); err != nil {
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

const (
	// drsInterval is how often fully automated DRS looks at the cluster.
	drsInterval = 5 * time.Minute
	// drsCooldown keeps DRS from moving the same machine again soon (no ping-pong).
	drsCooldown = 30 * time.Minute
)

// RunDRS carries out recommendations by itself while DRS is fully automated. Each member
// acts only for the machines it runs, one live migration at a time, so members never move
// the same machine twice; the overloaded host sheds its own load.
func (s *Server) RunDRS(ctx context.Context) {
	if s.drs == nil || s.cluster == nil {
		return
	}
	moved := map[string]time.Time{}
	t := time.NewTicker(drsInterval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
		s.autoBalance(ctx, moved, time.Now())
	}
}

func (s *Server) autoBalance(ctx context.Context, moved map[string]time.Time, now time.Time) {
	cfg := s.drs.GetConfig()
	if !cfg.Enabled || cfg.Mode != drs.ModeFullyAutomated || !s.cluster.Clustered() {
		return
	}
	for _, j := range s.jobs.List() {
		if j.State == jobs.Running && (j.Kind == "migrate" || j.Kind == "move") {
			return // one at a time; the next pass sees the new balance
		}
	}
	hosts, domains := s.collectClusterPlacements(ctx)
	recs := s.drs.Evaluate(ctx, hosts, domains).Recommendations
	slices.SortStableFunc(recs, func(a, b drs.Recommendation) int { return b.Priority - a.Priority })
	for _, rec := range recs {
		if rec.SourceHostID != s.cluster.SelfID() || now.Sub(moved[rec.DomainUUID]) < drsCooldown {
			continue
		}
		moved[rec.DomainUUID] = now
		rw := &statusRecorder{ResponseWriter: discard{}, status: http.StatusOK}
		s.applyRecommendation(ctx, rw, rec, auth.Identity{Name: "drs", Role: auth.Admin})
		s.record(activity.Entry{
			User: "drs", Method: http.MethodPost, Path: "/api/v1/domains/" + rec.DomainUUID + "/move",
			Status: rw.status, Error: rw.errorMessage(),
		})
		return
	}
}

// discard is a ResponseWriter for answers nobody reads (automatic actions).
type discard struct{}

func (discard) Header() http.Header         { return http.Header{} }
func (discard) Write(b []byte) (int, error) { return len(b), nil }
func (discard) WriteHeader(int)             {}

// drsPlacement suggests the member a new machine fits best while DRS places machines
// (partially or fully automated). No suggestion: 204.
func (s *Server) drsPlacement(w http.ResponseWriter, r *http.Request) {
	if s.drs == nil || s.cluster == nil || !s.cluster.Clustered() {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	if cfg := s.drs.GetConfig(); !cfg.Enabled || cfg.Mode == drs.ModeManual {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	mib, _ := strconv.ParseUint(r.URL.Query().Get("memoryMiB"), 10, 64)
	hosts, _ := s.collectClusterPlacements(r.Context())
	best, ok := drs.Placement(hosts, mib<<20)
	if !ok {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	s.writeJSON(w, http.StatusOK, map[string]any{
		"hostId": best.ID, "hostName": best.Name, "cpuPct": best.CPUPct, "memPct": best.MemPct,
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
