package placement

import (
	"cmp"
	"fmt"
	"slices"
	"strings"
)

// VMRef represents a virtual machine's current placement and status.
type VMRef struct {
	UUID    string `json:"uuid"`
	Name    string `json:"name"`
	HostID  string `json:"hostId"`
	Running bool   `json:"running"`
}

// Violation represents an active or potential rule violation.
type Violation struct {
	RuleID      string   `json:"ruleId"`
	RuleName    string   `json:"ruleName"`
	Kind        Kind     `json:"kind"`
	Hard        bool     `json:"hard"`
	DomainUUIDs []string `json:"domainUuids"`
	HostID      string   `json:"hostId"`
	Message     string   `json:"message"`
}

// Violations evaluates all active rules across RUNNING VMs and returns any placement violations.
// Rules and VMs are evaluated deterministically (rule order, then rule.Domains order).
func Violations(rules []Rule, vms []VMRef) []Violation {
	var out []Violation

	runningVMs := make(map[string]VMRef)
	for _, vm := range vms {
		if vm.Running {
			runningVMs[vm.UUID] = vm
		}
	}

	for _, r := range rules {
		if !r.Enabled {
			continue
		}
		hard := r.Strength == Must || r.Strength == ""

		switch r.Kind {
		case VMAntiAffinity:
			// Anti-affinity violation: >1 rule VM on the same host
			hostVMs := make(map[string][]string)
			for _, d := range r.Domains {
				if vm, ok := runningVMs[d]; ok {
					hostVMs[vm.HostID] = append(hostVMs[vm.HostID], d)
				}
			}

			seenHosts := make(map[string]bool)
			for _, d := range r.Domains {
				if vm, ok := runningVMs[d]; ok {
					h := vm.HostID
					if len(hostVMs[h]) > 1 && !seenHosts[h] {
						seenHosts[h] = true
						out = append(out, Violation{
							RuleID:      r.ID,
							RuleName:    r.Name,
							Kind:        r.Kind,
							Hard:        hard,
							DomainUUIDs: hostVMs[h],
							HostID:      h,
							Message:     fmt.Sprintf("anti-affinity rule %q violated: multiple VMs (%s) are running on host %s", r.Name, strings.Join(hostVMs[h], ", "), h),
						})
					}
				}
			}

		case VMAffinity:
			// Affinity violation: rule VMs spread over >1 host
			var runningDomains []string
			var distinctHosts []string
			for _, d := range r.Domains {
				if vm, ok := runningVMs[d]; ok {
					runningDomains = append(runningDomains, d)
					if !slices.Contains(distinctHosts, vm.HostID) {
						distinctHosts = append(distinctHosts, vm.HostID)
					}
				}
			}

			if len(distinctHosts) > 1 {
				out = append(out, Violation{
					RuleID:      r.ID,
					RuleName:    r.Name,
					Kind:        r.Kind,
					Hard:        hard,
					DomainUUIDs: runningDomains,
					HostID:      "",
					Message:     fmt.Sprintf("affinity rule %q violated: VMs (%s) are spread across multiple hosts (%s)", r.Name, strings.Join(runningDomains, ", "), strings.Join(distinctHosts, ", ")),
				})
			}

		case VMHost:
			// VMHost violation: must run on Hosts or must not run on Hosts
			for _, d := range r.Domains {
				if vm, ok := runningVMs[d]; ok {
					if r.Exclude {
						// vm_host exclude: a rule VM on a host in Hosts
						if slices.Contains(r.Hosts, vm.HostID) {
							out = append(out, Violation{
								RuleID:      r.ID,
								RuleName:    r.Name,
								Kind:        r.Kind,
								Hard:        hard,
								DomainUUIDs: []string{d},
								HostID:      vm.HostID,
								Message:     fmt.Sprintf("vm-host rule %q violated: VM %s is running on excluded host %s", r.Name, d, vm.HostID),
							})
						}
					} else {
						// vm_host must: a rule VM on a host not in Hosts
						if !slices.Contains(r.Hosts, vm.HostID) {
							out = append(out, Violation{
								RuleID:      r.ID,
								RuleName:    r.Name,
								Kind:        r.Kind,
								Hard:        hard,
								DomainUUIDs: []string{d},
								HostID:      vm.HostID,
								Message:     fmt.Sprintf("vm-host rule %q violated: VM %s is on host %s, which is not in allowed hosts (%s)", r.Name, d, vm.HostID, strings.Join(r.Hosts, ", ")),
							})
						}
					}
				}
			}
		}
	}

	return out
}

// Check simulates placing uuid on targetHost and returns human-readable messages of any
// broken hard or soft placement constraints. Rules not containing uuid are ignored.
func Check(rules []Rule, vms []VMRef, uuid, targetHost string) (hard, soft []string) {
	hard = []string{}
	soft = []string{}

	for _, r := range rules {
		if !r.Enabled || !slices.Contains(r.Domains, uuid) {
			continue
		}
		isHard := r.Strength == Must || r.Strength == ""

		switch r.Kind {
		case VMAntiAffinity:
			var conflicting []string
			for _, vm := range vms {
				if vm.Running && vm.UUID != uuid && vm.HostID == targetHost && slices.Contains(r.Domains, vm.UUID) {
					conflicting = append(conflicting, vm.UUID)
				}
			}
			if len(conflicting) > 0 {
				msg := fmt.Sprintf("anti-affinity rule %q: placing %s on host %s conflicts with %s already on that host", r.Name, uuid, targetHost, strings.Join(conflicting, ", "))
				if isHard {
					hard = append(hard, msg)
				} else {
					soft = append(soft, msg)
				}
			}

		case VMAffinity:
			var otherHosts []string
			var otherVMs []string
			for _, vm := range vms {
				if vm.Running && vm.UUID != uuid && slices.Contains(r.Domains, vm.UUID) {
					if vm.HostID != targetHost {
						otherVMs = append(otherVMs, vm.UUID)
						if !slices.Contains(otherHosts, vm.HostID) {
							otherHosts = append(otherHosts, vm.HostID)
						}
					}
				}
			}
			if len(otherVMs) > 0 {
				msg := fmt.Sprintf("affinity rule %q: placing %s on host %s separates it from %s on other hosts (%s)", r.Name, uuid, targetHost, strings.Join(otherVMs, ", "), strings.Join(otherHosts, ", "))
				if isHard {
					hard = append(hard, msg)
				} else {
					soft = append(soft, msg)
				}
			}

		case VMHost:
			if r.Exclude {
				if slices.Contains(r.Hosts, targetHost) {
					msg := fmt.Sprintf("vm-host rule %q: host %s is excluded for VM %s", r.Name, targetHost, uuid)
					if isHard {
						hard = append(hard, msg)
					} else {
						soft = append(soft, msg)
					}
				}
			} else {
				if !slices.Contains(r.Hosts, targetHost) {
					msg := fmt.Sprintf("vm-host rule %q: host %s is not in allowed hosts (%s) for VM %s", r.Name, targetHost, strings.Join(r.Hosts, ", "), uuid)
					if isHard {
						hard = append(hard, msg)
					} else {
						soft = append(soft, msg)
					}
				}
			}
		}
	}

	return hard, soft
}

// Rank partitions candidate hosts into ok and blocked. Candidates that violate any must rule
// go to blocked with reasons. Remaining candidates keep their relative order but are
// stable-sorted by the number of satisfied should rules, most first.
func Rank(rules []Rule, vms []VMRef, uuid string, candidates []string) ([]string, map[string][]string) {
	blocked := make(map[string][]string)
	var ok []string
	scores := make(map[string]int)

	var shouldRules []Rule
	for _, r := range rules {
		if r.Enabled && r.Strength == Should && slices.Contains(r.Domains, uuid) {
			shouldRules = append(shouldRules, r)
		}
	}

	for _, c := range candidates {
		hard, soft := Check(rules, vms, uuid, c)
		if len(hard) > 0 {
			blocked[c] = hard
		} else {
			ok = append(ok, c)
			scores[c] = len(shouldRules) - len(soft)
		}
	}

	slices.SortStableFunc(ok, func(a, b string) int {
		return cmp.Compare(scores[b], scores[a])
	})

	return ok, blocked
}

// Conflicts detects static hard contradictions among placement rules:
// (a) a must vm_affinity and a must vm_anti_affinity sharing >= 2 domains;
// (b) for one VM, a must vm_host group fully covered by a must vm_host exclude group;
// (c) pigeonhole: a must anti-affinity rule with more domains than hostCount (or than
// the union of its VMs' must vm_host groups when all its VMs are pinned).
func Conflicts(rules []Rule, hostCount int) []string {
	var conflicts []string

	var affRules, antiRules []Rule
	type vmHostRules struct {
		includes []Rule
		excludes []Rule
	}
	vmRules := make(map[string]*vmHostRules)

	for _, r := range rules {
		if !r.Enabled || (r.Strength != Must && r.Strength != "") {
			continue
		}
		switch r.Kind {
		case VMAffinity:
			affRules = append(affRules, r)
		case VMAntiAffinity:
			antiRules = append(antiRules, r)
		case VMHost:
			for _, d := range r.Domains {
				vr := vmRules[d]
				if vr == nil {
					vr = &vmHostRules{}
					vmRules[d] = vr
				}
				if r.Exclude {
					vr.excludes = append(vr.excludes, r)
				} else {
					vr.includes = append(vr.includes, r)
				}
			}
		}
	}

	// (a) Must affinity & must anti-affinity sharing >= 2 domains
	for _, aff := range affRules {
		for _, anti := range antiRules {
			var common []string
			for _, d := range aff.Domains {
				if slices.Contains(anti.Domains, d) {
					common = append(common, d)
				}
			}
			if len(common) >= 2 {
				conflicts = append(conflicts, fmt.Sprintf("affinity rule %q and anti-affinity rule %q conflict: both contain %s", aff.Name, anti.Name, strings.Join(common, ", ")))
			}
		}
	}

	// (b) For one VM, a must vm_host group fully covered by a must vm_host exclude group
	vmKeys := make([]string, 0, len(vmRules))
	for k := range vmRules {
		vmKeys = append(vmKeys, k)
	}
	slices.Sort(vmKeys)

	for _, d := range vmKeys {
		vr := vmRules[d]
		if len(vr.includes) == 0 {
			continue
		}
		for _, inc := range vr.includes {
			for _, exc := range vr.excludes {
				allCovered := true
				for _, h := range inc.Hosts {
					if !slices.Contains(exc.Hosts, h) {
						allCovered = false
						break
					}
				}
				if allCovered {
					conflicts = append(conflicts, fmt.Sprintf("VM %s has must vm_host rule %q (hosts: %s) fully covered by exclude rule %q (hosts: %s)", d, inc.Name, strings.Join(inc.Hosts, ", "), exc.Name, strings.Join(exc.Hosts, ", ")))
				}
			}
		}
	}

	// (c) Pigeonhole: must anti-affinity rule with more domains than hostCount
	// (or than the union of its VMs' must vm_host groups when all its VMs are pinned).
	for _, anti := range antiRules {
		if hostCount > 0 && len(anti.Domains) > hostCount {
			conflicts = append(conflicts, fmt.Sprintf("anti-affinity rule %q has %d VMs, which exceeds host count (%d)", anti.Name, len(anti.Domains), hostCount))
			continue
		}

		allPinned := true
		vmAllowedHosts := make(map[string][]string)

		for _, d := range anti.Domains {
			vr := vmRules[d]
			if vr == nil || len(vr.includes) == 0 {
				allPinned = false
				break
			}
			allowed := slices.Clone(vr.includes[0].Hosts)
			for _, inc := range vr.includes[1:] {
				allowed = slices.DeleteFunc(allowed, func(h string) bool {
					return !slices.Contains(inc.Hosts, h)
				})
			}
			for _, exc := range vr.excludes {
				allowed = slices.DeleteFunc(allowed, func(h string) bool {
					return slices.Contains(exc.Hosts, h)
				})
			}
			if len(allowed) == 0 {
				allPinned = false
				break
			}
			vmAllowedHosts[d] = allowed
		}

		if allPinned {
			unionHostsMap := make(map[string]bool)
			for _, d := range anti.Domains {
				for _, h := range vmAllowedHosts[d] {
					unionHostsMap[h] = true
				}
			}
			if len(anti.Domains) > len(unionHostsMap) {
				unionList := make([]string, 0, len(unionHostsMap))
				for h := range unionHostsMap {
					unionList = append(unionList, h)
				}
				slices.Sort(unionList)
				conflicts = append(conflicts, fmt.Sprintf("anti-affinity rule %q has %d VMs, but all VMs are pinned to a union of only %d hosts (%s)", anti.Name, len(anti.Domains), len(unionHostsMap), strings.Join(unionList, ", ")))
			}
		}
	}

	return conflicts
}

// FilterHATargets filters and orders candidates for HA VM restarts.
// Returns allowed candidates (surviving all must rules) and preferred candidates
// (allowed candidates also satisfying every should rule, preserving relative order).
func FilterHATargets(rules []Rule, uuid string, candidates []string, runningOn map[string]string) (allowed, preferred []string) {
	var vms []VMRef
	for u, h := range runningOn {
		if u != uuid {
			vms = append(vms, VMRef{UUID: u, HostID: h, Running: true})
		}
	}

	allowed = []string{}
	preferred = []string{}

	for _, c := range candidates {
		hard, soft := Check(rules, vms, uuid, c)
		if len(hard) == 0 {
			allowed = append(allowed, c)
			if len(soft) == 0 {
				preferred = append(preferred, c)
			}
		}
	}

	return allowed, preferred
}
