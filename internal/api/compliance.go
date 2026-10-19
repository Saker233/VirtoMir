package api

import (
	"context"
	"net/http"
	"os"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/onurbaydas/virtomir/internal/activity"
	"github.com/onurbaydas/virtomir/internal/cluster"
	"github.com/onurbaydas/virtomir/internal/compliance"
)

func (s *Server) complianceRoutes(mux *routeMux) {
	mux.HandleFunc("GET /api/v1/compliance/checks", s.getComplianceChecks)
	mux.HandleFunc("POST /api/v1/compliance/checks/run", s.runComplianceChecks)
	mux.HandleFunc("GET /api/v1/compliance/fips", s.getFIPS)
	mux.HandleFunc("GET /api/v1/compliance/summary", s.getComplianceSummary)
	mux.HandleFunc("GET /api/v1/cluster/compliance", s.getClusterCompliance)
	mux.HandleFunc("GET /api/v1/cluster/peer/compliance", s.peerCompliance)
	mux.HandleFunc("POST /api/v1/activity/verify", s.verifyAuditLog)
	mux.HandleFunc("GET /api/v1/activity/export", s.exportAuditLog)
}

func (s *Server) hostIdentifiers() (string, string) {
	hostID := ""
	if s.cluster != nil && s.cluster.Clustered() {
		hostID = s.cluster.SelfID()
	}
	hostName := s.hostName
	if hostName == "" {
		hostName, _ = os.Hostname()
	}
	if hostID == "" {
		hostID = hostName
	}
	return hostID, hostName
}

// scanner returns the compliance scanner, initializing the fallback scanner
// exactly once so concurrent requests never race on the assignment.
func (s *Server) scanner() *compliance.Scanner {
	s.scannerOnce.Do(func() {
		if s.complianceScanner == nil {
			s.complianceScanner = compliance.NewScanner(nil)
		}
	})
	return s.complianceScanner
}

func (s *Server) getComplianceChecks(w http.ResponseWriter, r *http.Request) {
	hostID, hostName := s.hostIdentifiers()
	force := r.URL.Query().Get("refresh") == "1"
	comp := s.scanner().Scan(r.Context(), hostID, hostName, force)
	s.writeJSON(w, http.StatusOK, comp)
}

func (s *Server) runComplianceChecks(w http.ResponseWriter, r *http.Request) {
	hostID, hostName := s.hostIdentifiers()
	comp := s.scanner().ScanForced(r.Context(), hostID, hostName)
	s.writeJSON(w, http.StatusOK, comp)
}

func (s *Server) getFIPS(w http.ResponseWriter, _ *http.Request) {
	s.writeJSON(w, http.StatusOK, s.FIPSInfo())
}

// ComplianceSummary summarizes the host's compliance posture, FIPS state, and audit log health.
type ComplianceSummary struct {
	Score        int                 `json:"score"`
	TotalChecks  int                 `json:"totalChecks"`
	PassedCount  int                 `json:"passedCount"`
	FailedCount  int                 `json:"failedCount"`
	WarningCount int                 `json:"warningCount"`
	FIPS         compliance.FIPSInfo `json:"fips"`
	AuditValid   bool                `json:"auditValid"`
}

// auditValidity reports whether the audit log hash chain currently verifies.
// Verify() walks the whole log (O(n)), so the result is cached for 30 s to keep
// frequent GET summary calls cheap. With no audit log configured the chain
// cannot be proven, so it reports not-valid rather than optimistically true.
func (s *Server) auditValidity() bool {
	if s.activity == nil {
		return false
	}
	s.auditVerifyMu.Lock()
	defer s.auditVerifyMu.Unlock()
	if time.Since(s.auditVerifyAt) < auditVerifyTTL {
		return s.auditVerified
	}
	res, err := s.activity.Verify()
	s.auditVerified = err == nil && res.Valid
	s.auditVerifyAt = time.Now()
	return s.auditVerified
}

func (s *Server) getComplianceSummary(w http.ResponseWriter, r *http.Request) {
	hostID, hostName := s.hostIdentifiers()
	comp := s.scanner().Scan(r.Context(), hostID, hostName, false)

	summary := ComplianceSummary{
		Score:        comp.Score,
		TotalChecks:  comp.TotalCount,
		PassedCount:  comp.PassedCount,
		FailedCount:  comp.FailedCount,
		WarningCount: comp.WarningCount,
		FIPS:         s.FIPSInfo(),
		AuditValid:   s.auditValidity(),
	}
	s.writeJSON(w, http.StatusOK, summary)
}

func (s *Server) getClusterCompliance(w http.ResponseWriter, r *http.Request) {
	hostID, hostName := s.hostIdentifiers()
	local := s.scanner().Scan(r.Context(), hostID, hostName, false)
	out := []compliance.HostCompliance{local}

	if s.cluster != nil && s.cluster.Clustered() {
		members, _ := s.cluster.Members()
		var mu sync.Mutex
		var wg sync.WaitGroup
		for _, node := range members {
			if node.ID == s.cluster.SelfID() {
				continue
			}
			wg.Add(1)
			go func(n cluster.Node) {
				defer wg.Done()
				c, cancel := context.WithTimeout(r.Context(), 10*time.Second)
				defer cancel()
				var hc compliance.HostCompliance
				if err := s.peer(n).Call(c, http.MethodGet, "/api/v1/cluster/peer/compliance", nil, &hc); err != nil {
					mu.Lock()
					// Report unreachable peers explicitly instead of returning a
					// silently partial aggregation: no results and Score -1 marks
					// the host as unreachable in the response.
					out = append(out, compliance.HostCompliance{HostID: n.ID, HostName: n.Name, Score: -1})
					mu.Unlock()
					s.log.Warn("cluster compliance: peer fetch", "peer", n.Name, "err", err)
					return
				}
				mu.Lock()
				out = append(out, hc)
				mu.Unlock()
			}(node)
		}
		wg.Wait()
	}

	sort.Slice(out, func(i, j int) bool {
		return out[i].HostID < out[j].HostID
	})

	s.writeJSON(w, http.StatusOK, out)
}

func (s *Server) peerCompliance(w http.ResponseWriter, r *http.Request) {
	if s.notPeer(w, r) {
		return
	}
	hostID, hostName := s.hostIdentifiers()
	comp := s.scanner().Scan(r.Context(), hostID, hostName, false)
	s.writeJSON(w, http.StatusOK, comp)
}

func (s *Server) verifyAuditLog(w http.ResponseWriter, _ *http.Request) {
	if s.activity == nil {
		s.writeJSON(w, http.StatusOK, activity.VerificationResult{Valid: true})
		return
	}
	res, err := s.activity.Verify()
	if err != nil {
		s.writeError(w, err)
		return
	}
	s.writeJSON(w, http.StatusOK, res)
}

func (s *Server) exportAuditLog(w http.ResponseWriter, r *http.Request) {
	if s.activity == nil {
		s.writeJSON(w, http.StatusOK, map[string]any{"entries": []activity.Entry{}})
		return
	}
	format := strings.ToLower(r.URL.Query().Get("format"))
	if format == "" {
		format = "jsonl"
	}
	if format != "jsonl" && format != "csv" {
		s.writeJSON(w, http.StatusBadRequest, map[string]string{"error": "unsupported format: expected jsonl or csv"})
		return
	}

	id := identity(r)
	s.record(activity.Entry{
		User:   id.Name,
		Token:  id.Token,
		Remote: r.RemoteAddr,
		Method: r.Method,
		Path:   r.URL.Path,
		Status: http.StatusOK,
	})

	switch format {
	case "csv":
		w.Header().Set("Content-Type", "text/csv; charset=utf-8")
		w.Header().Set("Content-Disposition", "attachment; filename=virtomir-activity.csv")
	case "jsonl":
		w.Header().Set("Content-Type", "application/x-ndjson; charset=utf-8")
		w.Header().Set("Content-Disposition", "attachment; filename=virtomir-activity.jsonl")
	}

	if err := s.activity.Export(w, format); err != nil {
		s.log.Warn("export activity log", "err", err)
	}
}
