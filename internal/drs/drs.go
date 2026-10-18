// Package drs provides Distributed Resource Scheduling (DRS) and workload rebalancing
// across cluster nodes in VirtoMir. It monitors host CPU, RAM, and I/O pressure,
// calculates standard deviation of cluster utilization (Cluster Balance Score),
// evaluates VM-VM and VM-Host Affinity / Anti-Affinity rules, and generates actionable
// rebalancing recommendations with priority ratings.
package drs

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"math"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"
)

// DRSMode represents the operational automation level of DRS.
type DRSMode string

const (
	// ModeManual generates recommendations and asks admins for 1-click execution.
	ModeManual DRSMode = "manual"
	// ModePartiallyAutomated automatically places new VMs on optimal hosts and recommends rebalances.
	ModePartiallyAutomated DRSMode = "partially_automated"
	// ModeFullyAutomated rebalances cluster workloads automatically when thresholds are exceeded.
	ModeFullyAutomated DRSMode = "fully_automated"
)

// RuleKind defines the type of placement affinity constraint.
type RuleKind string

const (
	// AntiAffinityVMVM prevents specified VMs from running on the same physical host.
	AntiAffinityVMVM RuleKind = "vm_anti_affinity"
	// AffinityVMVM keeps specified VMs collocated on the same physical host.
	AffinityVMVM RuleKind = "vm_affinity"
	// AffinityVMHost pins specified VMs to a preferred or mandatory target host.
	AffinityVMHost RuleKind = "vm_host_affinity"
)

// AffinityRule defines an affinity or anti-affinity constraint.
type AffinityRule struct {
	ID         string   `json:"id"`
	Name       string   `json:"name"`
	Kind       RuleKind `json:"kind"`
	Enabled    bool     `json:"enabled"`
	Mandatory  bool     `json:"mandatory"` // hard constraint vs soft preference
	Domains    []string `json:"domains"`   // VM UUIDs
	TargetHost string   `json:"targetHost,omitempty"` // for vm_host_affinity
}

// HostMetrics represents the current utilization metrics of a cluster node.
type HostMetrics struct {
	ID        string  `json:"id"`
	Name      string  `json:"name"`
	Reachable bool    `json:"reachable"`
	CPUPct    float64 `json:"cpuPct"`    // 0.0 to 100.0
	MemPct    float64 `json:"memPct"`    // 0.0 to 100.0
	MemUsedB  uint64  `json:"memUsedB"`
	MemTotalB uint64  `json:"memTotalB"`
	VMCount   int     `json:"vmCount"`
}

// DomainPlacement represents a VM's current host assignment and load.
type DomainPlacement struct {
	UUID     string  `json:"uuid"`
	Name     string  `json:"name"`
	HostID   string  `json:"hostId"`
	State    string  `json:"state"` // running, shut_off, etc.
	CPUPct   float64 `json:"cpuPct"`
	MemUsedB uint64  `json:"memUsedB"`
}

// Recommendation represents a DRS proposed live migration action.
type Recommendation struct {
	ID           string    `json:"id"`
	Priority     int       `json:"priority"` // 1 (lowest) to 5 (critical)
	DomainUUID   string    `json:"domainUuid"`
	DomainName   string    `json:"domainName"`
	SourceHostID string    `json:"sourceHostId"`
	SourceName   string    `json:"sourceName"`
	TargetHostID string    `json:"targetHostId"`
	TargetName   string    `json:"targetName"`
	Reason       string    `json:"reason"`
	ExpectedGain float64   `json:"expectedGain"` // estimated improvement in balance score (0-100)
	CreatedAt    time.Time `json:"createdAt"`
}

// Status represents the complete DRS state exposed to the UI and API.
type Status struct {
	Enabled             bool             `json:"enabled"`
	Mode                DRSMode          `json:"mode"`
	BalanceScore        float64          `json:"balanceScore"` // 0.0 to 100.0 (100% = perfectly balanced)
	CPUStdDev           float64          `json:"cpuStdDev"`
	MemStdDev           float64          `json:"memStdDev"`
	ThresholdStdDev     float64          `json:"thresholdStdDev"` // default 15.0%
	Hosts               []HostMetrics    `json:"hosts"`
	Recommendations     []Recommendation `json:"recommendations"`
	Rules               []AffinityRule   `json:"rules"`
	LastEvaluationTime  time.Time        `json:"lastEvaluationTime"`
}

// Config stores persisted DRS configuration.
type Config struct {
	Enabled         bool           `json:"enabled"`
	Mode            DRSMode        `json:"mode"`
	ThresholdStdDev float64        `json:"thresholdStdDev"`
	Rules           []AffinityRule `json:"rules"`
}

// Engine manages DRS state, evaluations, and rules.
type Engine struct {
	path string
	mu   sync.RWMutex
	cfg  Config
}

// New creates or loads the DRS engine.
func New(stateDir string) (*Engine, error) {
	path := filepath.Join(stateDir, "drs.json")
	e := &Engine{
		path: path,
		cfg: Config{
			Enabled:         true,
			Mode:            ModeManual,
			ThresholdStdDev: 15.0,
			Rules:           []AffinityRule{},
		},
	}

	raw, err := os.ReadFile(path)
	if err == nil {
		var loaded Config
		if json.Unmarshal(raw, &loaded) == nil {
			if loaded.Mode == "" {
				loaded.Mode = ModeManual
			}
			if loaded.ThresholdStdDev <= 0 {
				loaded.ThresholdStdDev = 15.0
			}
			e.cfg = loaded
		}
	} else if !errors.Is(err, fs.ErrNotExist) {
		return nil, fmt.Errorf("read drs config: %w", err)
	}

	return e, nil
}

func (e *Engine) saveLocked() error {
	if e.path == "" {
		return nil
	}
	b, err := json.MarshalIndent(e.cfg, "", "  ")
	if err != nil {
		return err
	}
	tmp := e.path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, e.path)
}

// GetConfig returns current DRS configuration.
func (e *Engine) GetConfig() Config {
	e.mu.RLock()
	defer e.mu.RUnlock()
	return e.cfg
}

// SetConfig updates DRS mode and thresholds.
func (e *Engine) SetConfig(cfg Config) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	if cfg.ThresholdStdDev <= 0 {
		cfg.ThresholdStdDev = 15.0
	}
	if cfg.Mode == "" {
		cfg.Mode = ModeManual
	}
	e.cfg.Enabled = cfg.Enabled
	e.cfg.Mode = cfg.Mode
	e.cfg.ThresholdStdDev = cfg.ThresholdStdDev
	return e.saveLocked()
}

// AddRule adds an affinity or anti-affinity rule.
func (e *Engine) AddRule(rule AffinityRule) (AffinityRule, error) {
	e.mu.Lock()
	defer e.mu.Unlock()

	if strings.TrimSpace(rule.Name) == "" {
		return rule, errors.New("rule name is required")
	}
	if rule.Kind != AntiAffinityVMVM && rule.Kind != AffinityVMVM && rule.Kind != AffinityVMHost {
		return rule, fmt.Errorf("unsupported rule kind %q", rule.Kind)
	}
	if len(rule.Domains) < 1 {
		return rule, errors.New("at least one VM must be assigned to the rule")
	}
	if rule.Kind == AntiAffinityVMVM && len(rule.Domains) < 2 {
		return rule, errors.New("anti-affinity rule requires at least two VMs")
	}
	if rule.Kind == AffinityVMHost && strings.TrimSpace(rule.TargetHost) == "" {
		return rule, errors.New("vm-host affinity rule requires a target host")
	}

	rule.ID = randomHex(8)
	rule.Enabled = true
	e.cfg.Rules = append(e.cfg.Rules, rule)
	if err := e.saveLocked(); err != nil {
		return rule, err
	}
	return rule, nil
}

// DeleteRule removes an affinity rule by ID.
func (e *Engine) DeleteRule(id string) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	idx := slices.IndexFunc(e.cfg.Rules, func(r AffinityRule) bool { return r.ID == id })
	if idx < 0 {
		return fmt.Errorf("rule %q not found", id)
	}
	e.cfg.Rules = slices.Delete(e.cfg.Rules, idx, idx+1)
	return e.saveLocked()
}

// Evaluate analyzes the cluster state, verifies rules, calculates balance score and generates recommendations.
func (e *Engine) Evaluate(ctx context.Context, hosts []HostMetrics, domains []DomainPlacement) Status {
	e.mu.RLock()
	cfg := e.cfg
	rules := append([]AffinityRule(nil), cfg.Rules...)
	e.mu.RUnlock()

	status := Status{
		Enabled:            cfg.Enabled,
		Mode:               cfg.Mode,
		ThresholdStdDev:    cfg.ThresholdStdDev,
		Hosts:              hosts,
		Rules:              rules,
		Recommendations:    []Recommendation{},
		LastEvaluationTime: time.Now(),
	}

	reachableHosts := make([]HostMetrics, 0, len(hosts))
	for _, h := range hosts {
		if h.Reachable {
			reachableHosts = append(reachableHosts, h)
		}
	}

	if len(reachableHosts) < 2 {
		// Standalone or single reachable host has a static 100% score (no rebalance possible)
		status.BalanceScore = 100.0
		return status
	}

	// 1. Calculate Mean and Standard Deviation for CPU & Memory
	var totalCPU, totalMem float64
	for _, h := range reachableHosts {
		totalCPU += h.CPUPct
		totalMem += h.MemPct
	}
	meanCPU := totalCPU / float64(len(reachableHosts))
	meanMem := totalMem / float64(len(reachableHosts))

	var varCPU, varMem float64
	for _, h := range reachableHosts {
		varCPU += math.Pow(h.CPUPct-meanCPU, 2)
		varMem += math.Pow(h.MemPct-meanMem, 2)
	}
	cpuStdDev := math.Sqrt(varCPU / float64(len(reachableHosts)))
	memStdDev := math.Sqrt(varMem / float64(len(reachableHosts)))

	status.CPUStdDev = math.Round(cpuStdDev*10) / 10
	status.MemStdDev = math.Round(memStdDev*10) / 10

	// Combined imbalance: max of CPU and Memory skew
	maxDev := math.Max(cpuStdDev, memStdDev)
	// Balance Score: 100 - (maxDev * 2) capped in [0, 100]
	balance := math.Max(0.0, math.Min(100.0, 100.0-(maxDev*2.0)))
	status.BalanceScore = math.Round(balance*10) / 10

	if !cfg.Enabled {
		return status
	}

	// 2. Check rule violations first (Top priority)
	hostByID := make(map[string]HostMetrics, len(hosts))
	for _, h := range hosts {
		hostByID[h.ID] = h
	}

	domainByID := make(map[string]DomainPlacement, len(domains))
	for _, d := range domains {
		domainByID[d.UUID] = d
	}

	// Map of hostID -> []DomainPlacement
	vmsOnHost := make(map[string][]DomainPlacement)
	for _, d := range domains {
		if d.State == "running" {
			vmsOnHost[d.HostID] = append(vmsOnHost[d.HostID], d)
		}
	}

	// Check Anti-Affinity rules
	for _, rule := range rules {
		if !rule.Enabled || rule.Kind != AntiAffinityVMVM {
			continue
		}
		// Group rule domains by host
		hostOfDomain := make(map[string]string)
		for _, vmID := range rule.Domains {
			if vm, ok := domainByID[vmID]; ok && vm.State == "running" {
				hostOfDomain[vmID] = vm.HostID
			}
		}

		// Count collisions
		collisionsOnHost := make(map[string][]string)
		for vmID, hID := range hostOfDomain {
			collisionsOnHost[hID] = append(collisionsOnHost[hID], vmID)
		}

		for hID, vmIDs := range collisionsOnHost {
			if len(vmIDs) > 1 {
				// Violation! Recommend moving all but the first VM to another reachable host
				for _, culpritID := range vmIDs[1:] {
					culprit := domainByID[culpritID]
					// Find target host with least load that doesn't violate rules
					target := findOptimalHost(reachableHosts, hID, rule, domains, culprit)
					if target.ID != "" {
						status.Recommendations = append(status.Recommendations, Recommendation{
							ID:           randomHex(8),
							Priority:     5, // Highest priority: Rule violation
							DomainUUID:   culprit.UUID,
							DomainName:   culprit.Name,
							SourceHostID: hID,
							SourceName:   hostByID[hID].Name,
							TargetHostID: target.ID,
							TargetName:   target.Name,
							Reason:       fmt.Sprintf("Anti-affinity violation in rule %q: multiple protected VMs on %s", rule.Name, hostByID[hID].Name),
							ExpectedGain: 15.0,
							CreatedAt:    time.Now(),
						})
					}
				}
			}
		}
	}

	// 3. Rebalance based on load skew if standard deviation exceeds threshold
	if maxDev > cfg.ThresholdStdDev {
		// Identify most overloaded host and most idle host
		var mostLoaded, leastLoaded HostMetrics
		for i, h := range reachableHosts {
			if i == 0 || (h.CPUPct+h.MemPct) > (mostLoaded.CPUPct+mostLoaded.MemPct) {
				mostLoaded = h
			}
			if i == 0 || (h.CPUPct+h.MemPct) < (leastLoaded.CPUPct+leastLoaded.MemPct) {
				leastLoaded = h
			}
		}

		if mostLoaded.ID != leastLoaded.ID && len(vmsOnHost[mostLoaded.ID]) > 0 {
			// Find a suitable VM to migrate from mostLoaded to leastLoaded
			candidates := vmsOnHost[mostLoaded.ID]
			// Sort candidates by moderate resource footprint (not too large, not zero)
			slices.SortFunc(candidates, func(a, b DomainPlacement) int {
				return int(b.MemUsedB - a.MemUsedB)
			})

			for _, candidate := range candidates {
				// Verify candidate does not break mandatory rules on target
				if canMigrate(candidate, mostLoaded.ID, leastLoaded.ID, rules, domains) {
					priority := 3
					if maxDev > 25.0 {
						priority = 4
					}
					gain := math.Min(25.0, maxDev*0.7)

					status.Recommendations = append(status.Recommendations, Recommendation{
						ID:           randomHex(8),
						Priority:     priority,
						DomainUUID:   candidate.UUID,
						DomainName:   candidate.Name,
						SourceHostID: mostLoaded.ID,
						SourceName:   mostLoaded.Name,
						TargetHostID: leastLoaded.ID,
						TargetName:   leastLoaded.Name,
						Reason: fmt.Sprintf("Cluster skew: %s is overloaded (CPU: %.1f%%, RAM: %.1f%%) while %s is underutilized",
							mostLoaded.Name, mostLoaded.CPUPct, mostLoaded.MemPct, leastLoaded.Name),
						ExpectedGain: math.Round(gain*10) / 10,
						CreatedAt:    time.Now(),
					})
					break // Propose top recommendation per evaluation cycle
				}
			}
		}
	}

	return status
}

func findOptimalHost(hosts []HostMetrics, excludeHostID string, rule AffinityRule, allVMs []DomainPlacement, targetVM DomainPlacement) HostMetrics {
	var best HostMetrics
	lowestLoad := math.MaxFloat64

	for _, h := range hosts {
		if h.ID == excludeHostID || !h.Reachable {
			continue
		}
		// Check if h already hosts any VM from rule.Domains
		violates := false
		for _, vm := range allVMs {
			if vm.HostID == h.ID && vm.UUID != targetVM.UUID && slices.Contains(rule.Domains, vm.UUID) {
				violates = true
				break
			}
		}
		if violates {
			continue
		}

		combinedLoad := h.CPUPct + h.MemPct
		if combinedLoad < lowestLoad {
			lowestLoad = combinedLoad
			best = h
		}
	}
	return best
}

func canMigrate(vm DomainPlacement, fromHost, toHost string, rules []AffinityRule, allVMs []DomainPlacement) bool {
	for _, rule := range rules {
		if !rule.Enabled || !slices.Contains(rule.Domains, vm.UUID) {
			continue
		}
		if rule.Kind == AntiAffinityVMVM {
			// Ensure no other domain in rule.Domains is running on toHost
			for _, other := range allVMs {
				if other.HostID == toHost && other.UUID != vm.UUID && slices.Contains(rule.Domains, other.UUID) {
					return false
				}
			}
		} else if rule.Kind == AffinityVMHost {
			if rule.Mandatory && rule.TargetHost != toHost {
				return false
			}
		}
	}
	return true
}

func randomHex(bytes int) string {
	b := make([]byte, bytes)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}
