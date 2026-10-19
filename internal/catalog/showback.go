// Showback (ADR-0065) turns what the hosts run into per-project money: machines bill
// vCPU-hour and GiB-RAM-hour for the hours they run, and GiB-disk-month for the disks
// they hold provisioned, running or not. The report only states what VirtoMir measured
// at generation time; it never estimates usage it cannot see.
package catalog

import (
	"fmt"
	"math"
	"sort"
	"strings"
	"time"

	"github.com/onurbaydas/virtomir/internal/project"
	"github.com/onurbaydas/virtomir/internal/virt"
)

// MachineProvider supplies the machines a showback report prices.
type MachineProvider interface {
	ListDomains() ([]virt.Domain, error)
}

// DiskProvider is what a MachineProvider must also offer to price disk space; the
// virt.Client already does. Without it machines bill for compute time only.
type DiskProvider interface {
	DiskBytes(uuids []string) (map[string]uint64, error)
}

// UnassignedProjectName labels the machines without project metadata (ADR-0060).
const UnassignedProjectName = "Unassigned"

// VMCost bills one machine for one billing period.
type VMCost struct {
	UUID         string  `json:"uuid"`
	Name         string  `json:"name"`
	ProjectID    string  `json:"projectId"`
	VCPUs        int     `json:"vcpus"`
	MemoryMiB    uint64  `json:"memoryMiB"`
	DiskGiB      uint64  `json:"diskGiB"`
	State        string  `json:"state"`
	RuntimeHours float64 `json:"runtimeHours"`
	VCPUCost     float64 `json:"vcpuCost"`
	RAMCost      float64 `json:"ramCost"`
	DiskCost     float64 `json:"diskCost"`
	TotalCost    float64 `json:"totalCost"`
}

// ProjectCost bundles a project's machines and their bill for the period.
type ProjectCost struct {
	ProjectID      string   `json:"projectId"`
	ProjectName    string   `json:"projectName"`
	MachineCount   int      `json:"machineCount"`
	TotalVCPUs     int      `json:"totalVcpus"`
	TotalMemoryMiB uint64   `json:"totalMemoryMiB"`
	TotalDiskGiB   uint64   `json:"totalDiskGiB"`
	VCPUCost       float64  `json:"vcpuCost"`
	RAMCost        float64  `json:"ramCost"`
	DiskCost       float64  `json:"diskCost"`
	TotalCost      float64  `json:"totalCost"`
	Machines       []VMCost `json:"machines"`
}

// ShowbackReport prices every machine of one billing period (a calendar month).
type ShowbackReport struct {
	Period        string        `json:"period"` // "YYYY-MM"
	Year          int           `json:"year"`
	Month         int           `json:"month"`
	GeneratedAt   time.Time     `json:"generatedAt"`
	Currency      string        `json:"currency"`
	Pricing       PricingConfig `json:"pricing"`
	TotalCost     float64       `json:"totalCost"`
	TotalMachines int           `json:"totalMachines"`
	Projects      []ProjectCost `json:"projects"`
}

// HoursInMonth is the real length of that month in hours: 672, 720 or 744.
func HoursInMonth(year int, month time.Month) int {
	return time.Date(year, month+1, 0, 0, 0, 0, 0, time.UTC).Day() * 24
}

// round2 rounds a cost to whole cents.
func round2(v float64) float64 {
	return math.Round(v*100) / 100
}

// projectName resolves a project's display name; machines without project metadata
// bill under Unassigned.
func projectName(projects *project.Store, id string) string {
	if id == "" {
		return UnassignedProjectName
	}
	if projects != nil {
		if p, ok := projects.Get(id); ok && p.Name != "" {
			return p.Name
		}
	}
	return id
}

// isRunning tells whether the machine is up on a pCPU this hour: libvirt counts
// running and blocked (up but waiting on I/O) as active.
func isRunning(state virt.DomainState) bool {
	return state == virt.StateRunning || state == virt.StateBlocked
}

// CalculateShowback prices every machine of year-month against pricing, grouped by
// project. Machines without project metadata bill under "Unassigned"; with
// filterProject set, only that project's machines are included. Running machines bill
// the whole month's hours; stopped machines bill their provisioned disks only.
func CalculateShowback(provider MachineProvider, projects *project.Store, pricing PricingConfig, year int, month time.Month, filterProject string) (ShowbackReport, error) {
	if provider == nil {
		return ShowbackReport{}, fmt.Errorf("no machine provider is available")
	}
	doms, err := provider.ListDomains()
	if err != nil {
		return ShowbackReport{}, fmt.Errorf("list machines: %w", err)
	}
	hours := HoursInMonth(year, month)

	// Disk capacity per machine, one bulk query when the provider can answer it.
	disks := map[string]uint64{}
	if dp, ok := provider.(DiskProvider); ok {
		uuids := make([]string, 0, len(doms))
		for _, d := range doms {
			uuids = append(uuids, strings.ToLower(d.UUID))
		}
		if sizes, err := dp.DiskBytes(uuids); err == nil {
			disks = sizes
		}
	}

	names := map[string]string{}
	byProject := map[string][]VMCost{}
	for _, d := range doms {
		pid := d.Project
		if filterProject != "" && pid != filterProject {
			continue
		}
		names[pid] = projectName(projects, pid)
		mib := d.MaxMemoryKiB >> 10
		if mib == 0 {
			mib = d.MemoryKiB >> 10
		}
		runtime := 0.0
		if isRunning(d.State) {
			runtime = float64(hours)
		}
		vm := VMCost{
			UUID:         d.UUID,
			Name:         d.Name,
			ProjectID:    pid,
			VCPUs:        d.VCPUs,
			MemoryMiB:    mib,
			DiskGiB:      disks[strings.ToLower(d.UUID)] >> 30,
			State:        string(d.State),
			RuntimeHours: runtime,
		}
		vm.VCPUCost = round2(float64(vm.VCPUs) * runtime * pricing.VCPUHour)
		vm.RAMCost = round2(float64(vm.MemoryMiB) / 1024.0 * runtime * pricing.RAMGiBHour)
		// Provisioned disk space bills for the whole period, running or not.
		vm.DiskCost = round2(float64(vm.DiskGiB) * pricing.DiskGiBMonth)
		vm.TotalCost = round2(vm.VCPUCost + vm.RAMCost + vm.DiskCost)
		byProject[pid] = append(byProject[pid], vm)
	}

	out := ShowbackReport{
		Period:      fmt.Sprintf("%04d-%02d", year, int(month)),
		Year:        year,
		Month:       int(month),
		GeneratedAt: time.Now().UTC(),
		Currency:    pricing.Currency,
		Pricing:     pricing,
		Projects:    []ProjectCost{},
	}
	for pid, vms := range byProject {
		sort.Slice(vms, func(i, j int) bool { return vms[i].Name < vms[j].Name })
		pc := ProjectCost{ProjectID: pid, ProjectName: names[pid], Machines: vms, MachineCount: len(vms)}
		for _, vm := range vms {
			pc.TotalVCPUs += vm.VCPUs
			pc.TotalMemoryMiB += vm.MemoryMiB
			pc.TotalDiskGiB += vm.DiskGiB
			pc.VCPUCost += vm.VCPUCost
			pc.RAMCost += vm.RAMCost
			pc.DiskCost += vm.DiskCost
			pc.TotalCost += vm.TotalCost
		}
		pc.VCPUCost, pc.RAMCost, pc.DiskCost = round2(pc.VCPUCost), round2(pc.RAMCost), round2(pc.DiskCost)
		pc.TotalCost = round2(pc.TotalCost)
		out.Projects = append(out.Projects, pc)
		out.TotalCost += pc.TotalCost
		out.TotalMachines += pc.MachineCount
	}
	sort.Slice(out.Projects, func(i, j int) bool {
		if out.Projects[i].ProjectName == out.Projects[j].ProjectName {
			return out.Projects[i].ProjectID < out.Projects[j].ProjectID
		}
		return out.Projects[i].ProjectName < out.Projects[j].ProjectName
	})
	out.TotalCost = round2(out.TotalCost)
	return out, nil
}
