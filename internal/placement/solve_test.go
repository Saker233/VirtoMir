package placement

import (
	"slices"
	"strings"
	"testing"
)

func TestViolations(t *testing.T) {
	vms := []VMRef{
		{UUID: "vm-1", Name: "Web 1", HostID: "host-1", Running: true},
		{UUID: "vm-2", Name: "Web 2", HostID: "host-1", Running: true},
		{UUID: "vm-3", Name: "DB 1", HostID: "host-2", Running: true},
		{UUID: "vm-4", Name: "DB 2", HostID: "host-3", Running: true},
		{UUID: "vm-stopped", Name: "Stopped", HostID: "host-1", Running: false},
	}

	// 1. Anti-affinity: vm-1 and vm-2 both on host-1 -> violation
	antiRule := Rule{
		ID:       "anti1",
		Name:     "Separate Web",
		Kind:     VMAntiAffinity,
		Enabled:  true,
		Strength: Must,
		Domains:  []string{"vm-1", "vm-2"},
	}
	viols := Violations([]Rule{antiRule}, vms)
	if len(viols) != 1 {
		t.Fatalf("expected 1 violation, got %d", len(viols))
	}
	if !viols[0].Hard || viols[0].HostID != "host-1" || len(viols[0].DomainUUIDs) != 2 {
		t.Errorf("unexpected anti-affinity violation: %+v", viols[0])
	}

	// Disabled anti-affinity rule must be ignored
	antiRule.Enabled = false
	if viols := Violations([]Rule{antiRule}, vms); len(viols) != 0 {
		t.Errorf("expected disabled rule to yield 0 violations, got %d", len(viols))
	}
	antiRule.Enabled = true

	// Stopped VM in anti-affinity does not violate
	antiWithStopped := Rule{
		ID:       "anti-stopped",
		Name:     "Anti with stopped",
		Kind:     VMAntiAffinity,
		Enabled:  true,
		Strength: Must,
		Domains:  []string{"vm-1", "vm-stopped"},
	}
	if viols := Violations([]Rule{antiWithStopped}, vms); len(viols) != 0 {
		t.Errorf("stopped VM should not cause anti-affinity violation, got %d", len(viols))
	}

	// 2. Affinity: vm-3 on host-2 and vm-4 on host-3 -> violation (spread over >1 host)
	affRule := Rule{
		ID:       "aff1",
		Name:     "Collocate DBs",
		Kind:     VMAffinity,
		Enabled:  true,
		Strength: Should,
		Domains:  []string{"vm-3", "vm-4"},
	}
	viols = Violations([]Rule{affRule}, vms)
	if len(viols) != 1 {
		t.Fatalf("expected 1 affinity violation, got %d", len(viols))
	}
	if viols[0].Hard {
		t.Errorf("expected soft violation (Hard=false) for Should strength")
	}
	if len(viols[0].DomainUUIDs) != 2 {
		t.Errorf("expected 2 domains in affinity violation: %+v", viols[0])
	}

	// Collocated affinity has 0 violations
	vmsCollocated := []VMRef{
		{UUID: "vm-3", HostID: "host-2", Running: true},
		{UUID: "vm-4", HostID: "host-2", Running: true},
	}
	if viols := Violations([]Rule{affRule}, vmsCollocated); len(viols) != 0 {
		t.Errorf("collocated VMs should have 0 affinity violations, got %d", len(viols))
	}

	// 3. VMHost must: vm-1 on host-1, allowed only on host-2, host-3
	hostMustRule := Rule{
		ID:       "h-must",
		Name:     "Pin Web 1 to Host 2/3",
		Kind:     VMHost,
		Enabled:  true,
		Strength: Must,
		Domains:  []string{"vm-1"},
		Hosts:    []string{"host-2", "host-3"},
	}
	viols = Violations([]Rule{hostMustRule}, vms)
	if len(viols) != 1 || viols[0].HostID != "host-1" || !viols[0].Hard {
		t.Errorf("unexpected vm_host must violation: %+v", viols)
	}

	// 4. VMHost exclude: vm-1 on host-1, excluded from host-1
	hostExcludeRule := Rule{
		ID:       "h-exclude",
		Name:     "Exclude Web 1 from Host 1",
		Kind:     VMHost,
		Enabled:  true,
		Strength: Must,
		Domains:  []string{"vm-1"},
		Hosts:    []string{"host-1"},
		Exclude:  true,
	}
	viols = Violations([]Rule{hostExcludeRule}, vms)
	if len(viols) != 1 || viols[0].HostID != "host-1" || !viols[0].Hard {
		t.Errorf("unexpected vm_host exclude violation: %+v", viols)
	}

	// Deterministic order: rule order, then rule.Domains order
	allRules := []Rule{antiRule, affRule, hostMustRule, hostExcludeRule}
	allViols := Violations(allRules, vms)
	if len(allViols) != 4 {
		t.Fatalf("expected 4 total violations, got %d", len(allViols))
	}
	if allViols[0].RuleID != "anti1" || allViols[1].RuleID != "aff1" || allViols[2].RuleID != "h-must" || allViols[3].RuleID != "h-exclude" {
		t.Errorf("violations not in deterministic rule order: %+v", allViols)
	}
}

func TestCheckDistinguishesHardVsSoft(t *testing.T) {
	vms := []VMRef{
		{UUID: "vm-1", HostID: "h1", Running: true},
		{UUID: "vm-2", HostID: "h2", Running: true},
		{UUID: "vm-3", HostID: "h3", Running: true},
	}

	rules := []Rule{
		{
			ID:       "r-anti-hard",
			Name:     "Must Separate vm-1 and vm-2",
			Kind:     VMAntiAffinity,
			Enabled:  true,
			Strength: Must,
			Domains:  []string{"vm-1", "vm-2"},
		},
		{
			ID:       "r-host-soft",
			Name:     "Should avoid h2 for vm-1",
			Kind:     VMHost,
			Enabled:  true,
			Strength: Should,
			Domains:  []string{"vm-1"},
			Hosts:    []string{"h2"},
			Exclude:  true,
		},
		{
			ID:       "r-unrelated",
			Name:     "Must collocate vm-2 and vm-3",
			Kind:     VMAffinity,
			Enabled:  true,
			Strength: Must,
			Domains:  []string{"vm-2", "vm-3"},
		},
	}

	// Simulating placing vm-1 on h2:
	// - Hard: breaks r-anti-hard because vm-2 is on h2
	// - Soft: breaks r-host-soft because h2 is excluded
	// - r-unrelated: does NOT contain vm-1, ignored
	hard, soft := Check(rules, vms, "vm-1", "h2")
	if len(hard) != 1 {
		t.Fatalf("expected 1 hard violation, got %d: %v", len(hard), hard)
	}
	if !strings.Contains(hard[0], "Must Separate vm-1 and vm-2") {
		t.Errorf("hard message does not name rule: %s", hard[0])
	}

	if len(soft) != 1 {
		t.Fatalf("expected 1 soft violation, got %d: %v", len(soft), soft)
	}
	if !strings.Contains(soft[0], "Should avoid h2 for vm-1") {
		t.Errorf("soft message does not name rule: %s", soft[0])
	}

	// Moving vm-1 to h4 (clean host with no conflicting VMs and not excluded)
	hardClean, softClean := Check(rules, vms, "vm-1", "h4")
	if len(hardClean) != 0 || len(softClean) != 0 {
		t.Errorf("expected clean check on h4, got hard=%v, soft=%v", hardClean, softClean)
	}
}

func TestRank(t *testing.T) {
	vms := []VMRef{
		{UUID: "vm-1", HostID: "h-source", Running: true},
		{UUID: "vm-anti", HostID: "h-blocked", Running: true},
		{UUID: "vm-aff1", HostID: "h-best", Running: true},
		{UUID: "vm-aff2", HostID: "h-best", Running: true},
	}

	rules := []Rule{
		// Hard must rule: vm-1 must not run on h-blocked
		{
			ID:       "r-anti",
			Name:     "Must anti-affinity with vm-anti",
			Kind:     VMAntiAffinity,
			Enabled:  true,
			Strength: Must,
			Domains:  []string{"vm-1", "vm-anti"},
		},
		// Soft should rule 1: should be with vm-aff1
		{
			ID:       "r-aff1",
			Name:     "Prefer collocated with vm-aff1",
			Kind:     VMAffinity,
			Enabled:  true,
			Strength: Should,
			Domains:  []string{"vm-1", "vm-aff1"},
		},
		// Soft should rule 2: should be with vm-aff2
		{
			ID:       "r-aff2",
			Name:     "Prefer collocated with vm-aff2",
			Kind:     VMAffinity,
			Enabled:  true,
			Strength: Should,
			Domains:  []string{"vm-1", "vm-aff2"},
		},
	}

	// Candidate list has h-second first, h-best second, h-blocked third
	candidates := []string{"h-second", "h-best", "h-blocked"}
	ok, blocked := Rank(rules, vms, "vm-1", candidates)

	// h-blocked must be in blocked map naming the rule
	if reasons, isBlocked := blocked["h-blocked"]; !isBlocked || len(reasons) == 0 {
		t.Fatalf("expected h-blocked to be blocked, got: %+v", blocked)
	} else if !strings.Contains(reasons[0], "Must anti-affinity with vm-anti") {
		t.Errorf("blocked reason does not name rule: %s", reasons[0])
	}

	// ok candidates must contain h-best and h-second.
	// h-best satisfies 2 should rules, h-second satisfies 0.
	// So h-best must be sorted first!
	if len(ok) != 2 {
		t.Fatalf("expected 2 ok candidates, got %d: %v", len(ok), ok)
	}
	if ok[0] != "h-best" || ok[1] != "h-second" {
		t.Errorf("expected [h-best, h-second], got: %v", ok)
	}
}

func TestConflicts(t *testing.T) {
	// Satisfiable rule set: no conflicts
	satisfiable := []Rule{
		{
			ID:       "r1",
			Name:     "Affinity 1",
			Kind:     VMAffinity,
			Enabled:  true,
			Strength: Must,
			Domains:  []string{"vm-1", "vm-2"},
		},
		{
			ID:       "r2",
			Name:     "Anti-affinity 1",
			Kind:     VMAntiAffinity,
			Enabled:  true,
			Strength: Must,
			Domains:  []string{"vm-2", "vm-3"}, // only 1 shared domain
		},
	}
	if confs := Conflicts(satisfiable, 3); len(confs) != 0 {
		t.Errorf("expected 0 conflicts on satisfiable set, got %v", confs)
	}

	// Class (a): must vm_affinity and must vm_anti_affinity sharing >= 2 domains
	classA := []Rule{
		{
			ID:       "r-aff",
			Name:     "Affinity Pair",
			Kind:     VMAffinity,
			Enabled:  true,
			Strength: Must,
			Domains:  []string{"vm-1", "vm-2", "vm-3"},
		},
		{
			ID:       "r-anti",
			Name:     "Anti-affinity Pair",
			Kind:     VMAntiAffinity,
			Enabled:  true,
			Strength: Must,
			Domains:  []string{"vm-1", "vm-2"},
		},
	}
	confs := Conflicts(classA, 3)
	if len(confs) != 1 || !strings.Contains(confs[0], "conflict") {
		t.Errorf("expected Class (a) conflict, got: %v", confs)
	}

	// Class (b): for one VM, a must vm_host group fully covered by a must vm_host exclude group
	classB := []Rule{
		{
			ID:       "r-inc",
			Name:     "Run on Host 1 or 2",
			Kind:     VMHost,
			Enabled:  true,
			Strength: Must,
			Domains:  []string{"vm-1"},
			Hosts:    []string{"h1", "h2"},
			Exclude:  false,
		},
		{
			ID:       "r-exc",
			Name:     "Exclude Host 1 and 2 and 3",
			Kind:     VMHost,
			Enabled:  true,
			Strength: Must,
			Domains:  []string{"vm-1"},
			Hosts:    []string{"h1", "h2", "h3"},
			Exclude:  true,
		},
	}
	confs = Conflicts(classB, 3)
	if len(confs) != 1 || !strings.Contains(confs[0], "fully covered") {
		t.Errorf("expected Class (b) conflict, got: %v", confs)
	}

	// Class (c) Pigeonhole:
	// 1. Must anti-affinity rule with more domains than hostCount
	classC1 := []Rule{
		{
			ID:       "r-anti-3",
			Name:     "Three VMs separate",
			Kind:     VMAntiAffinity,
			Enabled:  true,
			Strength: Must,
			Domains:  []string{"vm-1", "vm-2", "vm-3"},
		},
	}
	confs = Conflicts(classC1, 2) // 3 VMs on 2 hosts
	if len(confs) != 1 || !strings.Contains(confs[0], "exceeds host count") {
		t.Errorf("expected Class (c) hostCount pigeonhole conflict, got: %v", confs)
	}

	// 2. Or than the union of its VMs' must vm_host groups when all its VMs are pinned
	classC2 := []Rule{
		{
			ID:       "r-anti-pinned",
			Name:     "Three VMs anti-affinity",
			Kind:     VMAntiAffinity,
			Enabled:  true,
			Strength: Must,
			Domains:  []string{"vm-1", "vm-2", "vm-3"},
		},
		{
			ID:       "r-pin-1",
			Name:     "Pin vm-1 and vm-2",
			Kind:     VMHost,
			Enabled:  true,
			Strength: Must,
			Domains:  []string{"vm-1", "vm-2"},
			Hosts:    []string{"h1", "h2"},
		},
		{
			ID:       "r-pin-2",
			Name:     "Pin vm-3",
			Kind:     VMHost,
			Enabled:  true,
			Strength: Must,
			Domains:  []string{"vm-3"},
			Hosts:    []string{"h2"},
		},
	}
	// Union of allowed hosts is {"h1", "h2"} (2 hosts), but anti-affinity has 3 VMs.
	// Even with hostCount = 0 or 10, this must report conflict!
	confs = Conflicts(classC2, 0)
	if len(confs) != 1 || !strings.Contains(confs[0], "pinned to a union of only") {
		t.Errorf("expected Class (c) pinned union pigeonhole conflict, got: %v", confs)
	}
}

func TestFilterHATargets(t *testing.T) {
	// Candidate hosts: hA, hB, hC
	// vm-partner is running on hB
	runningOn := map[string]string{
		"vm-partner": "hB",
	}

	rules := []Rule{
		// Must anti-affinity with vm-partner: hB cannot be used!
		{
			ID:       "r-anti",
			Name:     "Must separate from partner",
			Kind:     VMAntiAffinity,
			Enabled:  true,
			Strength: Must,
			Domains:  []string{"vm-test", "vm-partner"},
		},
		// Should vm_host on hA: hC breaks should rule
		{
			ID:       "r-prefer-a",
			Name:     "Prefer host A",
			Kind:     VMHost,
			Enabled:  true,
			Strength: Should,
			Domains:  []string{"vm-test"},
			Hosts:    []string{"hA"},
		},
	}

	candidates := []string{"hA", "hB", "hC"}
	allowed, preferred := FilterHATargets(rules, "vm-test", candidates, runningOn)

	// hB is disallowed due to must anti-affinity
	if slices.Contains(allowed, "hB") {
		t.Errorf("expected hB to be disallowed, got allowed: %v", allowed)
	}
	if len(allowed) != 2 || allowed[0] != "hA" || allowed[1] != "hC" {
		t.Errorf("expected allowed [hA, hC], got: %v", allowed)
	}

	// hC violates the should rule, so only hA is preferred
	if len(preferred) != 1 || preferred[0] != "hA" {
		t.Errorf("expected preferred [hA], got: %v", preferred)
	}
}

func TestFilterHATargets_MustAntiAffinityAndShouldVMHost(t *testing.T) {
	// Candidate hosts: A, B, C
	// Partner VM running on candidate B
	runningOn := map[string]string{
		"vm-partner": "B",
	}

	rules := []Rule{
		// Must vm_anti_affinity with partner running on candidate B -> B is disallowed
		{
			ID:       "r-anti-b",
			Name:     "Separate from partner on B",
			Kind:     VMAntiAffinity,
			Enabled:  true,
			Strength: Must,
			Domains:  []string{"vm-self", "vm-partner"},
		},
		// Should vm_host preferring A -> C is deprioritized (allowed, but not preferred)
		{
			ID:       "r-should-host-a",
			Name:     "Prefer host A",
			Kind:     VMHost,
			Enabled:  true,
			Strength: Should,
			Domains:  []string{"vm-self"},
			Hosts:    []string{"A"},
		},
	}

	candidates := []string{"A", "B", "C"}
	allowed, preferred := FilterHATargets(rules, "vm-self", candidates, runningOn)

	// Candidate B is disallowed by hard must anti-affinity rule
	if slices.Contains(allowed, "B") {
		t.Errorf("expected candidate B to be disallowed, got allowed: %v", allowed)
	}
	// Candidate C is allowed (should rules are soft)
	if !slices.Contains(allowed, "C") {
		t.Errorf("expected candidate C to be allowed, got allowed: %v", allowed)
	}
	// Final allowed set should be [A, C]
	expectedAllowed := []string{"A", "C"}
	if !slices.Equal(allowed, expectedAllowed) {
		t.Errorf("allowed = %v, want %v", allowed, expectedAllowed)
	}

	// Should rule deprioritizes candidate C: only A satisfies all should rules
	expectedPreferred := []string{"A"}
	if !slices.Equal(preferred, expectedPreferred) {
		t.Errorf("preferred = %v, want %v", preferred, expectedPreferred)
	}
	if slices.Contains(preferred, "C") {
		t.Errorf("expected candidate C to be deprioritized (not in preferred), got preferred: %v", preferred)
	}
}

func TestEmptyStrengthTreatedAsMust(t *testing.T) {
	vms := []VMRef{
		{UUID: "vm-1", HostID: "host-1", Running: true},
		{UUID: "vm-2", HostID: "host-1", Running: true},
	}

	// Rule with empty Strength ("") defaults to Must
	ruleEmptyStrength := Rule{
		ID:       "rule-default-must",
		Name:     "Anti-affinity empty strength",
		Kind:     VMAntiAffinity,
		Enabled:  true,
		Strength: "", // empty, should be treated as Must (Hard=true)
		Domains:  []string{"vm-1", "vm-2"},
	}

	// 1. Violations check
	viols := Violations([]Rule{ruleEmptyStrength}, vms)
	if len(viols) != 1 {
		t.Fatalf("expected 1 violation, got %d", len(viols))
	}
	if !viols[0].Hard {
		t.Errorf("expected rule with empty strength to produce Hard=true violation")
	}

	// 2. Check check
	hard, soft := Check([]Rule{ruleEmptyStrength}, []VMRef{{UUID: "vm-2", HostID: "host-1", Running: true}}, "vm-1", "host-1")
	if len(hard) != 1 || len(soft) != 0 {
		t.Errorf("expected empty strength rule to be in hard violations, got hard=%v, soft=%v", hard, soft)
	}
}
