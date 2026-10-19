package catalog

import (
	"bytes"
	"encoding/csv"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/onurbaydas/virtomir/internal/project"
	"github.com/onurbaydas/virtomir/internal/virt"
)

// showbackProvider is a MachineProvider with fixed machines and disk sizes.
type showbackProvider struct {
	doms  []virt.Domain
	disks map[string]uint64
}

func (p *showbackProvider) ListDomains() ([]virt.Domain, error) {
	return p.doms, nil
}

func (p *showbackProvider) DiskBytes(uuids []string) (map[string]uint64, error) {
	out := map[string]uint64{}
	for _, u := range uuids {
		out[u] = p.disks[u]
	}
	return out, nil
}

func showbackTestProjects(t *testing.T) *project.Store {
	t.Helper()
	s, err := project.Open(t.TempDir() + "/projects.json")
	if err != nil {
		t.Fatalf("open project store: %v", err)
	}
	if _, err := s.Create(project.Project{ID: "proj-a", Name: "Alpha"}); err != nil {
		t.Fatalf("create project: %v", err)
	}
	if _, err := s.Create(project.Project{ID: "proj-b", Name: "Beta"}); err != nil {
		t.Fatalf("create project: %v", err)
	}
	return s
}

var testPricing = PricingConfig{VCPUHour: 0.05, RAMGiBHour: 0.01, DiskGiBMonth: 0.10, Currency: "USD"}

func TestHoursInMonth_MonthBoundary(t *testing.T) {
	cases := map[string]struct {
		year  int
		month time.Month
		want  int
	}{
		"january 31 days":       {2026, time.January, 744},
		"february 28 days":      {2026, time.February, 672},
		"leap february 29 days": {2024, time.February, 696},
		"april 30 days":         {2026, time.April, 720},
		"december 31 days":      {2026, time.December, 744},
	}
	for name, c := range cases {
		if got := HoursInMonth(c.year, c.month); got != c.want {
			t.Errorf("%s: HoursInMonth(%d, %s) = %d, want %d", name, c.year, c.month, got, c.want)
		}
	}
}

// TestCalculateShowback_CostMath verifies the manual arithmetic: 2 vCPU, 4 GiB RAM and
// 100 GiB disk for a 720-hour (30-day) month at 0.05/0.01/0.10.
func TestCalculateShowback_CostMath(t *testing.T) {
	projects := showbackTestProjects(t)
	p := &showbackProvider{
		doms: []virt.Domain{{
			UUID: "11111111-1111-1111-1111-111111111111", Name: "web-01",
			Project: "proj-a", State: virt.StateRunning, VCPUs: 2, MaxMemoryKiB: 4096 << 10,
		}},
		disks: map[string]uint64{"11111111-1111-1111-1111-111111111111": 100 << 30},
	}
	rep, err := CalculateShowback(p, projects, testPricing, 2026, time.June, "")
	if err != nil {
		t.Fatalf("CalculateShowback: %v", err)
	}
	if rep.Period != "2026-06" || rep.Year != 2026 || rep.Month != 6 {
		t.Errorf("period = %q (%d-%d), want 2026-06", rep.Period, rep.Year, rep.Month)
	}
	if rep.Currency != "USD" {
		t.Errorf("currency = %q, want USD", rep.Currency)
	}
	if len(rep.Projects) != 1 {
		t.Fatalf("got %d projects, want 1", len(rep.Projects))
	}
	pc := rep.Projects[0]
	if pc.ProjectID != "proj-a" || pc.ProjectName != "Alpha" {
		t.Errorf("project = %q/%q, want proj-a/Alpha", pc.ProjectID, pc.ProjectName)
	}
	if pc.TotalDiskGiB != 100 {
		t.Errorf("TotalDiskGiB = %d, want 100", pc.TotalDiskGiB)
	}
	// 2 vCPU × 720 h × 0.05 = 72.00
	if pc.VCPUCost != 72.0 {
		t.Errorf("VCPUCost = %.4f, want 72.00", pc.VCPUCost)
	}
	// 4 GiB × 720 h × 0.01 = 28.80
	if pc.RAMCost != 28.8 {
		t.Errorf("RAMCost = %.4f, want 28.80", pc.RAMCost)
	}
	// 100 GiB × 0.10 = 10.00
	if pc.DiskCost != 10.0 {
		t.Errorf("DiskCost = %.4f, want 10.00", pc.DiskCost)
	}
	if pc.TotalCost != 110.8 {
		t.Errorf("TotalCost = %.4f, want 110.80", pc.TotalCost)
	}
	if rep.TotalCost != 110.8 {
		t.Errorf("report TotalCost = %.4f, want 110.80", rep.TotalCost)
	}
	if vm := pc.Machines[0]; vm.RuntimeHours != 720 {
		t.Errorf("RuntimeHours = %v, want 720", vm.RuntimeHours)
	}
	if rep.TotalMachines != 1 {
		t.Errorf("TotalMachines = %d, want 1", rep.TotalMachines)
	}
}

func TestCalculateShowback_StoppedMachineBillsDiskOnly(t *testing.T) {
	projects := showbackTestProjects(t)
	p := &showbackProvider{
		doms: []virt.Domain{{
			UUID: "22222222-2222-2222-2222-222222222222", Name: "db-02",
			Project: "proj-b", State: virt.StateShutoff, VCPUs: 4, MaxMemoryKiB: 8192 << 10,
		}},
		disks: map[string]uint64{"22222222-2222-2222-2222-222222222222": 50 << 30},
	}
	rep, err := CalculateShowback(p, projects, testPricing, 2026, time.June, "")
	if err != nil {
		t.Fatalf("CalculateShowback: %v", err)
	}
	vm := rep.Projects[0].Machines[0]
	if vm.RuntimeHours != 0 {
		t.Errorf("RuntimeHours = %v, want 0", vm.RuntimeHours)
	}
	if vm.VCPUCost != 0 || vm.RAMCost != 0 {
		t.Errorf("stopped machine billed compute: %.2f / %.2f, want 0 / 0", vm.VCPUCost, vm.RAMCost)
	}
	if vm.DiskCost != 5.0 {
		t.Errorf("DiskCost = %.4f, want 5.00 (50 GiB × 0.10)", vm.DiskCost)
	}
	if vm.TotalCost != 5.0 {
		t.Errorf("TotalCost = %.4f, want 5.00", vm.TotalCost)
	}
}

// TestCalculateShowback_MonthBoundary checks the same one machine bills 31 vs 28 day
// months by their real hour counts.
func TestCalculateShowback_MonthBoundary(t *testing.T) {
	projects := showbackTestProjects(t)
	p := &showbackProvider{
		doms: []virt.Domain{{
			UUID: "33333333-3333-3333-3333-333333333333", Name: "app-01",
			Project: "proj-a", State: virt.StateRunning, VCPUs: 1, MaxMemoryKiB: 1024 << 10,
		}},
		disks: map[string]uint64{},
	}
	costMonthly := func(year int, month time.Month) float64 {
		rep, err := CalculateShowback(p, projects, testPricing, year, month, "")
		if err != nil {
			t.Fatalf("CalculateShowback: %v", err)
		}
		return rep.TotalCost
	}
	jan := costMonthly(2026, time.January)
	feb := costMonthly(2026, time.February)
	// 1 vCPU × 0.05 = 0.05/h, 1 GiB RAM × 0.01 = 0.01/h → 0.06 per hour, no disk.
	if got := jan - feb; got != (744-672)*0.06 {
		t.Errorf("Jan (%.2f) minus Feb (%.2f) = %.2f, want %.2f", jan, feb, got, (744-672)*0.06)
	}
	if feb != 672*0.06 {
		t.Errorf("February total = %.2f, want %.2f", feb, 672*0.06)
	}
}

func TestCalculateShowback_GroupsAndFiltersProjects(t *testing.T) {
	projects := showbackTestProjects(t)
	p := &showbackProvider{
		doms: []virt.Domain{
			{UUID: "a1", Name: "alpha-01", Project: "proj-a", State: virt.StateRunning, VCPUs: 2, MaxMemoryKiB: 2048 << 10},
			{UUID: "a2", Name: "alpha-02", Project: "proj-a", State: virt.StateShutoff, VCPUs: 2, MaxMemoryKiB: 2048 << 10},
			{UUID: "b1", Name: "beta-01", Project: "proj-b", State: virt.StateRunning, VCPUs: 1, MaxMemoryKiB: 1024 << 10},
			{UUID: "u1", Name: "orphan-01", State: virt.StateRunning, VCPUs: 1, MaxMemoryKiB: 1024 << 10},
		},
		disks: map[string]uint64{"a1": 10 << 30, "a2": 10 << 30, "b1": 20 << 30, "u1": 5 << 30},
	}
	rep, err := CalculateShowback(p, projects, testPricing, 2026, time.June, "")
	if err != nil {
		t.Fatalf("CalculateShowback: %v", err)
	}
	if len(rep.Projects) != 3 {
		t.Fatalf("got %d projects, want 3 (alpha, beta, unassigned)", len(rep.Projects))
	}
	var alpha, beta, unassigned *ProjectCost
	for i := range rep.Projects {
		switch rep.Projects[i].ProjectID {
		case "proj-a":
			alpha = &rep.Projects[i]
		case "proj-b":
			beta = &rep.Projects[i]
		case "":
			unassigned = &rep.Projects[i]
		}
	}
	if alpha == nil || beta == nil || unassigned == nil {
		t.Fatalf("missing projects in %+v", rep.Projects)
	}
	if alpha.ProjectName != "Alpha" || alpha.MachineCount != 2 || alpha.TotalVCPUs != 4 || alpha.TotalMemoryMiB != 4<<10 {
		t.Errorf("alpha = %+v, unexpected", *alpha)
	}
	if unassigned.ProjectName != UnassignedProjectName {
		t.Errorf("unassigned name = %q, want %q", unassigned.ProjectName, UnassignedProjectName)
	}
	if rep.TotalMachines != 4 || rep.TotalCost <= 0 {
		t.Errorf("totals: machines %d cost %.2f", rep.TotalMachines, rep.TotalCost)
	}

	// filterProject keeps only that project's machines.
	filtered, err := CalculateShowback(p, projects, testPricing, 2026, time.June, "proj-a")
	if err != nil {
		t.Fatalf("CalculateShowback filtered: %v", err)
	}
	if len(filtered.Projects) != 1 || filtered.Projects[0].ProjectID != "proj-a" || filtered.Projects[0].MachineCount != 2 {
		t.Errorf("filtered projects = %+v, want only proj-a with 2 machines", filtered.Projects)
	}
	if filtered.TotalCost != alpha.TotalCost {
		t.Errorf("filtered total = %.2f, want %.2f", filtered.TotalCost, alpha.TotalCost)
	}
}

func TestRenderCSV(t *testing.T) {
	rep := ShowbackReport{
		Period: "2026-06", Year: 2026, Month: 6, Currency: "USD",
		Projects: []ProjectCost{{
			ProjectID: "proj-a", ProjectName: "Alpha", MachineCount: 1,
			TotalVCPUs: 2, TotalMemoryMiB: 4096, TotalDiskGiB: 100,
			VCPUCost: 72.0, RAMCost: 28.8, DiskCost: 10.0, TotalCost: 110.8,
			Machines: []VMCost{{
				UUID: "u1", Name: "web,01", ProjectID: "proj-a", VCPUs: 2, MemoryMiB: 4096,
				DiskGiB: 100, State: "running", RuntimeHours: 720,
				VCPUCost: 72.0, RAMCost: 28.8, DiskCost: 10.0, TotalCost: 110.8,
			}},
		}},
	}
	b, err := RenderCSV(rep)
	if err != nil {
		t.Fatalf("RenderCSV: %v", err)
	}
	wantHeader := "ProjectID,ProjectName,VMName,VMUUID,State,VCPUs,RAM_MiB,Disk_GiB,RuntimeHours,VCPU_Cost,RAM_Cost,Disk_Cost,Total_Cost,Currency"
	lines := strings.Split(strings.TrimRight(string(b), "\n"), "\n")
	if lines[0] != wantHeader {
		t.Errorf("CSV header = %q, want %q", lines[0], wantHeader)
	}
	if len(lines) != 3 {
		t.Fatalf("got %d CSV rows, want 3 (header, machine, project total)", len(lines))
	}
	// RFC 4180: the machine name holds a comma, so the field is quoted.
	if !strings.Contains(lines[1], `"web,01"`) {
		t.Errorf("CSV did not quote VMName containing a comma: %q", lines[1])
	}
	if !strings.Contains(lines[2], "PROJECT TOTAL") || !strings.Contains(lines[2], "110.80") {
		t.Errorf("project total row wrong: %q", lines[2])
	}
	// The whole output parses back with the RFC 4180 reader.
	recs, err := csv.NewReader(bytes.NewReader(b)).ReadAll()
	if err != nil {
		t.Fatalf("output is not valid CSV: %v", err)
	}
	if len(recs) != 3 || len(recs[0]) != 14 {
		t.Errorf("parsed %d rows × %d cols, want 3 × 14", len(recs), len(recs[0]))
	}
}

func TestRenderJSON(t *testing.T) {
	rep := ShowbackReport{
		Period: "2026-06", Year: 2026, Month: 6, Currency: "USD",
		Pricing: testPricing, TotalCost: 110.8, TotalMachines: 1,
		Projects: []ProjectCost{{
			ProjectID: "proj-a", ProjectName: "Alpha", MachineCount: 1, TotalCost: 110.8,
			Machines: []VMCost{{UUID: "u1", Name: "web-01", ProjectID: "proj-a", TotalCost: 110.8}},
		}},
	}
	b, err := RenderJSON(rep)
	if err != nil {
		t.Fatalf("RenderJSON: %v", err)
	}
	var got ShowbackReport
	if err := json.Unmarshal(b, &got); err != nil {
		t.Fatalf("output is not valid JSON: %v", err)
	}
	if got.TotalCost != 110.8 || len(got.Projects) != 1 || got.Pricing.VCPUHour != 0.05 {
		t.Errorf("round trip mismatch: %+v", got)
	}
}

func TestRenderHTML(t *testing.T) {
	rep := ShowbackReport{
		Period: "2026-06", Year: 2026, Month: 6, GeneratedAt: time.Date(2026, 6, 1, 12, 0, 0, 0, time.UTC),
		Currency: "USD", Pricing: testPricing, TotalCost: 110.8, TotalMachines: 1,
		Projects: []ProjectCost{{
			ProjectID: "proj-a", ProjectName: "Alpha", MachineCount: 1,
			TotalVCPUs: 2, TotalMemoryMiB: 4096, TotalDiskGiB: 100,
			VCPUCost: 72.0, RAMCost: 28.8, DiskCost: 10.0, TotalCost: 110.8,
			Machines: []VMCost{{
				UUID: "u1", Name: "web-01", ProjectID: "proj-a", VCPUs: 2, MemoryMiB: 4096,
				DiskGiB: 100, State: "running", RuntimeHours: 720,
				VCPUCost: 72.0, RAMCost: 28.8, DiskCost: 10.0, TotalCost: 110.8,
			}},
		}},
	}
	b, err := RenderHTML(rep)
	if err != nil {
		t.Fatalf("RenderHTML: %v", err)
	}
	s := string(b)
	for _, want := range []string{
		"<!DOCTYPE html>",
		"Project Showback &amp; Cost Allocation Report",
		"June 2026",
		"Total Cost", "Active Projects", "Total Provisioned VMs", "Total vCPUs", "Total Memory", "Total Disk",
		"Pricing applied",
		"<table", "Alpha", "proj-a", "Share of Spend",
		"<details><summary>", "<details>", "web-01",
		"110.80",
		"@media print", "page-break-inside",
		"</html>",
	} {
		if !strings.Contains(s, want) {
			t.Errorf("HTML is missing %q", want)
		}
	}
}

func TestRenderHTML_EmptyReport(t *testing.T) {
	b, err := RenderHTML(ShowbackReport{Year: 2026, Month: 6, Currency: "USD", Projects: []ProjectCost{}})
	if err != nil {
		t.Fatalf("RenderHTML: %v", err)
	}
	s := string(b)
	if !strings.Contains(s, "No machines were billed") {
		t.Errorf("empty report is missing its note: %q", s)
	}
}
