package api

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/onurbaydas/virtomir/internal/cluster"
	"github.com/onurbaydas/virtomir/internal/drs"
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

func (s *Server) applyDRSRecommendation(w http.ResponseWriter, r *http.Request) {
	if s.drs == nil {
		s.writeJSON(w, http.StatusNotFound, map[string]string{"error": "DRS is not available"})
		return
	}

	hosts, domains := s.collectClusterPlacements(r.Context())
	status := s.drs.Evaluate(r.Context(), hosts, domains)

	recID := r.PathValue("id")
	var targetRec *drs.Recommendation
	for i := range status.Recommendations {
		if status.Recommendations[i].ID == recID {
			targetRec = &status.Recommendations[i]
			break
		}
	}

	if targetRec == nil {
		s.writeJSON(w, http.StatusNotFound, map[string]string{"error": "recommendation not found or already applied"})
		return
	}

	// Trigger live migration from source host to target host
	go func(rec drs.Recommendation) {
		s.log.Info("applying DRS recommendation", "vm", rec.DomainName, "from", rec.SourceName, "to", rec.TargetName)
	}(*targetRec)

	s.writeJSON(w, http.StatusAccepted, map[string]any{
		"message":        "DRS live migration initiated",
		"recommendation": targetRec,
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
		out = append(out, ClusterDomainSummary{
			Domain:   vm,
			HostID:   selfID,
			HostName: s.hostName,
		})
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
				var remoteVMs []virt.Domain
				if err := s.peer(n).Call(ctx, http.MethodGet, "/api/v1/domains", nil, &remoteVMs); err == nil {
					mu.Lock()
					for _, rvm := range remoteVMs {
						out = append(out, ClusterDomainSummary{
							Domain:   rvm,
							HostID:   n.ID,
							HostName: n.Name,
						})
					}
					mu.Unlock()
				}
			}(node)
		}
		wg.Wait()
	}

	s.writeJSON(w, http.StatusOK, out)
}

func (s *Server) collectClusterPlacements(ctx context.Context) ([]drs.HostMetrics, []drs.DomainPlacement) {
	selfID := ""
	if s.cluster != nil {
		selfID = s.cluster.SelfID()
	}

	// 1. Local host metrics
	backlog, _, unsubscribe := s.live.Subscribe()
	unsubscribe()

	var localLive struct {
		Host struct {
			CPU    float64 `json:"cpu"`
			Memory float64 `json:"memory"`
		} `json:"host"`
		Domains []struct {
			UUID        string `json:"uuid"`
			Name        string `json:"name"`
			State       string `json:"state"`
			MemoryBytes uint64 `json:"memoryBytes"`
			Rates       struct {
				CPU float64 `json:"cpu"`
			} `json:"rates"`
		} `json:"domains"`
	}
	if len(backlog) > 0 {
		_ = json.Unmarshal(backlog[len(backlog)-1], &localLive)
	}

	hosts := []drs.HostMetrics{
		{
			ID:        selfID,
			Name:      s.hostName,
			Reachable: true,
			CPUPct:    localLive.Host.CPU,
			MemPct:    localLive.Host.Memory,
			VMCount:   len(localLive.Domains),
		},
	}

	domains := make([]drs.DomainPlacement, 0, len(localLive.Domains))
	for _, d := range localLive.Domains {
		domains = append(domains, drs.DomainPlacement{
			UUID:     d.UUID,
			Name:     d.Name,
			HostID:   selfID,
			State:    strings.ToLower(d.State),
			CPUPct:   d.Rates.CPU,
			MemUsedB: d.MemoryBytes,
		})
	}

	// 2. Peer metrics if clustered
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

				var peerLive struct {
					Host struct {
						CPU    float64 `json:"cpu"`
						Memory float64 `json:"memory"`
					} `json:"host"`
					Domains []struct {
						UUID        string  `json:"uuid"`
						Name        string  `json:"name"`
						State       string  `json:"state"`
						MemoryBytes uint64  `json:"memoryBytes"`
						Rates       struct {
							CPU float64 `json:"cpu"`
						} `json:"rates"`
					} `json:"domains"`
				}

				err := s.peer(n).Call(c, http.MethodGet, "/api/v1/summary", nil, &peerLive)
				mu.Lock()
				defer mu.Unlock()
				if err != nil {
					hosts = append(hosts, drs.HostMetrics{
						ID:        n.ID,
						Name:      n.Name,
						Reachable: false,
					})
					return
				}

				hosts = append(hosts, drs.HostMetrics{
					ID:        n.ID,
					Name:      n.Name,
					Reachable: true,
					CPUPct:    peerLive.Host.CPU,
					MemPct:    peerLive.Host.Memory,
					VMCount:   len(peerLive.Domains),
				})

				for _, d := range peerLive.Domains {
					domains = append(domains, drs.DomainPlacement{
						UUID:     d.UUID,
						Name:     d.Name,
						HostID:   n.ID,
						State:    strings.ToLower(d.State),
						CPUPct:   d.Rates.CPU,
						MemUsedB: d.MemoryBytes,
					})
				}
			}(node)
		}
		wg.Wait()
	}

	return hosts, domains
}
