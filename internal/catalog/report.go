// Report renderers turn a showback calculation into the formats users read: an
// indented JSON for scripts, RFC 4180 CSV for spreadsheets and a self-contained HTML
// document in the style of internal/reports that also prints cleanly to PDF (ADR-0065).
package catalog

import (
	"bytes"
	"encoding/csv"
	"encoding/json"
	"fmt"
	"html"
	"strconv"
	"strings"
	"time"
)

// RenderJSON returns the showback report as indented JSON.
func RenderJSON(r ShowbackReport) ([]byte, error) {
	return json.MarshalIndent(r, "", "  ")
}

// RenderCSV returns the showback report as RFC 4180 CSV: one row per machine, then a
// project's total after its machines.
func RenderCSV(r ShowbackReport) ([]byte, error) {
	var buf bytes.Buffer
	w := csv.NewWriter(&buf)
	_ = w.Write([]string{"ProjectID", "ProjectName", "VMName", "VMUUID", "State", "VCPUs", "RAM_MiB",
		"Disk_GiB", "RuntimeHours", "VCPU_Cost", "RAM_Cost", "Disk_Cost", "Total_Cost", "Currency"})
	for _, pc := range r.Projects {
		for _, vm := range pc.Machines {
			_ = w.Write([]string{vm.ProjectID, pc.ProjectName, vm.Name, vm.UUID, vm.State,
				fmt.Sprintf("%d", vm.VCPUs), fmt.Sprintf("%d", vm.MemoryMiB), fmt.Sprintf("%d", vm.DiskGiB),
				fmt.Sprintf("%.2f", vm.RuntimeHours), fmt.Sprintf("%.2f", vm.VCPUCost),
				fmt.Sprintf("%.2f", vm.RAMCost), fmt.Sprintf("%.2f", vm.DiskCost),
				fmt.Sprintf("%.2f", vm.TotalCost), r.Currency})
		}
		_ = w.Write([]string{pc.ProjectID, pc.ProjectName, "PROJECT TOTAL", "", "",
			fmt.Sprintf("%d", pc.TotalVCPUs), fmt.Sprintf("%d", pc.TotalMemoryMiB), fmt.Sprintf("%d", pc.TotalDiskGiB),
			"", fmt.Sprintf("%.2f", pc.VCPUCost), fmt.Sprintf("%.2f", pc.RAMCost), fmt.Sprintf("%.2f", pc.DiskCost),
			fmt.Sprintf("%.2f", pc.TotalCost), r.Currency})
	}
	w.Flush()
	return buf.Bytes(), w.Error()
}

// monthName is the billing period's month for humans.
func monthName(m int) string {
	return time.Month(m).String()
}

// RenderHTML returns a self-contained showback document in the internal/reports style.
func RenderHTML(r ShowbackReport) ([]byte, error) {
	period := fmt.Sprintf("%s %d", monthName(r.Month), r.Year)
	var b strings.Builder
	b.WriteString(`<!DOCTYPE html>
<html lang="en"><head><meta charset="utf-8"><title>Project Showback &amp; Cost Allocation Report — ` + html.EscapeString(period) + `</title>
<style>
  body { margin: 0; padding: 32px; font: 13px/1.5 "IBM Plex Sans", system-ui, sans-serif; background: #f7f5f0; color: #18191c; }
  .head { border-bottom: 3px solid #c25e0e; padding-bottom: 14px; margin-bottom: 22px; display: flex; justify-content: space-between; align-items: flex-end; gap: 16px; }
  h1 { margin: 0 0 4px; font-size: 20px; text-transform: uppercase; letter-spacing: .02em; }
  h3 { margin: 24px 0 8px; font-size: 13px; text-transform: uppercase; }
  .summary { color: #4a4d54; }
  .logo { display: inline-block; font: 700 12px ui-monospace, monospace; padding: 4px 10px; background: #c25e0e; color: #fff; letter-spacing: .04em; margin-bottom: 10px; }
  .tag { font: 11px ui-monospace, monospace; border: 1px solid #18191c; padding: 3px 8px; white-space: nowrap; }
  .stat-grid { display: grid; grid-template-columns: repeat(auto-fit, minmax(180px, 1fr)); gap: 12px; margin-bottom: 8px; }
  .stat-box { border: 1px solid #bdb7ac; background: #fff; padding: 12px 14px; }
  .label { font: 11px ui-monospace, monospace; text-transform: uppercase; color: #4a4d54; }
  .val { font: 700 20px ui-monospace, monospace; margin: 2px 0; }
  .sub { font-size: 11px; color: #4a4d54; }
  .pricing { border: 1px dashed #bdb7ac; background: #fff; padding: 10px 14px; margin: 8px 0 16px; font: 11px ui-monospace, monospace; color: #4a4d54; }
  table { width: 100%; border-collapse: collapse; background: #fff; border: 1px solid #bdb7ac; }
  th { text-align: left; padding: 8px 10px; font: 11px ui-monospace, monospace; text-transform: uppercase; background: #e5e0d8; border-bottom: 1px solid #bdb7ac; }
  td { padding: 8px 10px; border-bottom: 1px solid #e5e0d8; vertical-align: top; }
  .mono { font-family: ui-monospace, monospace; }
  .text-dim { color: #85827a; }
  .num { text-align: right; white-space: nowrap; }
  details { margin: 4px 0 14px; }
  details > summary { cursor: pointer; font: 11px ui-monospace, monospace; text-transform: uppercase; color: #4a4d54; padding: 4px 0; }
  .bar { position: relative; height: 14px; background: #e5e0d8; min-width: 80px; }
  .bar > span { position: absolute; inset: 0; background: #c25e0e; }
  .bar > em { position: absolute; left: 6px; top: 0; font: 10px/14px ui-monospace, monospace; font-style: normal; color: #18191c; }
  .note { color: #4a4d54; font-size: 12px; margin-top: 12px; }
  @media print {
    body { padding: 0; background: #fff; }
    table { page-break-inside: avoid; }
    details { page-break-inside: avoid; }
  }
</style></head><body>
<div class="head"><div>
  <span class="logo">VM VIRTOMIR</span>
  <h1>Project Showback &amp; Cost Allocation Report</h1>
  <div class="summary">Billing period ` + html.EscapeString(period) + ` · generated ` +
		r.GeneratedAt.Local().Format("2006-01-02 15:04") + ` · ` + strconv.Itoa(len(r.Projects)) + ` project(s)</div>
</div><div class="tag">` + html.EscapeString(r.Currency) + ` · VirtoMir showback</div></div>`)

	memGiB, diskGiB := 0.0, 0.0
	totalVCPUs := 0
	for _, pc := range r.Projects {
		memGiB += float64(pc.TotalMemoryMiB) / 1024.0
		diskGiB += float64(pc.TotalDiskGiB)
		totalVCPUs += pc.TotalVCPUs
	}
	fmt.Fprintf(&b, `<div class="stat-grid">
<div class="stat-box"><div class="label">Total Cost</div><div class="val">%.2f %s</div><div class="sub">whole billing period</div></div>
<div class="stat-box"><div class="label">Active Projects</div><div class="val">%d</div><div class="sub">projects with machines</div></div>
<div class="stat-box"><div class="label">Total Provisioned VMs</div><div class="val">%d</div><div class="sub">machines billed</div></div>
<div class="stat-box"><div class="label">Total vCPUs</div><div class="val">%d</div><div class="sub">provisioned cores</div></div>
<div class="stat-box"><div class="label">Total Memory</div><div class="val">%.1f GiB</div><div class="sub">provisioned</div></div>
<div class="stat-box"><div class="label">Total Disk</div><div class="val">%.1f GiB</div><div class="sub">provisioned</div></div>
</div>
<div class="pricing">Pricing applied: vCPU-h %.3f · RAM GiB-h %.3f · Disk GiB-month %.3f · currency %s</div>`,
		r.TotalCost, html.EscapeString(r.Currency), len(r.Projects), r.TotalMachines, totalVCPUs, memGiB, diskGiB,
		r.Pricing.VCPUHour, r.Pricing.RAMGiBHour, r.Pricing.DiskGiBMonth, html.EscapeString(r.Currency))

	if len(r.Projects) == 0 {
		b.WriteString(`<p class="note">No machines were billed in this period.</p></body></html>`)
		return []byte(b.String()), nil
	}

	b.WriteString(`<h3>Projects</h3><table><thead><tr>
<th>Project</th><th>Name</th><th class="num">VMs</th><th class="num">vCPUs</th><th class="num">Memory (GiB)</th><th class="num">Disk (GiB)</th>
<th class="num">vCPU Cost</th><th class="num">RAM Cost</th><th class="num">Disk Cost</th><th class="num">Total</th><th>Share of Spend</th></tr></thead><tbody>`)
	for _, pc := range r.Projects {
		share := 0.0
		if r.TotalCost > 0 {
			share = pc.TotalCost / r.TotalCost * 100
		}
		fmt.Fprintf(&b, `<tr><td class="mono">%s</td><td><strong>%s</strong></td><td class="num">%d</td><td class="num">%d</td>
<td class="num">%.1f</td><td class="num">%.1f</td><td class="num">%.2f</td><td class="num">%.2f</td><td class="num">%.2f</td>
<td class="num"><strong>%.2f %s</strong></td><td><div class="bar"><span style="width:%.1f%%"></span><em>%.0f%%</em></div></td></tr>`,
			html.EscapeString(pc.ProjectID), html.EscapeString(pc.ProjectName), pc.MachineCount, pc.TotalVCPUs,
			float64(pc.TotalMemoryMiB)/1024.0, float64(pc.TotalDiskGiB),
			pc.VCPUCost, pc.RAMCost, pc.DiskCost, pc.TotalCost, html.EscapeString(r.Currency), share, share)
	}
	b.WriteString(`</tbody></table><h3>VM itemization</h3>`)
	for _, pc := range r.Projects {
		b.WriteString("<details><summary>" + html.EscapeString(pc.ProjectName) + " — " +
			fmt.Sprintf("%d VM(s) · %.2f %s", pc.MachineCount, pc.TotalCost, html.EscapeString(r.Currency)) + "</summary>")
		b.WriteString(`<table><thead><tr><th>VM</th><th>State</th><th class="num">vCPUs</th><th class="num">RAM (MiB)</th>
<th class="num">Disk (GiB)</th><th class="num">Runtime (h)</th><th class="num">vCPU</th><th class="num">RAM</th><th class="num">Disk</th><th class="num">Total</th></tr></thead><tbody>`)
		for _, vm := range pc.Machines {
			fmt.Fprintf(&b, `<tr><td><strong>%s</strong><br><small class="mono text-dim">%s</small></td><td class="mono">%s</td>
<td class="num">%d</td><td class="num">%d</td><td class="num">%d</td><td class="num">%.1f</td>
<td class="num">%.2f</td><td class="num">%.2f</td><td class="num">%.2f</td><td class="num"><strong>%.2f</strong></td></tr>`,
				html.EscapeString(vm.Name), html.EscapeString(vm.UUID), html.EscapeString(vm.State),
				vm.VCPUs, vm.MemoryMiB, vm.DiskGiB, vm.RuntimeHours,
				vm.VCPUCost, vm.RAMCost, vm.DiskCost, vm.TotalCost)
		}
		b.WriteString(`</tbody></table></details>`)
	}
	b.WriteString(`<p class="note">Showback bills vCPU and RAM by the hours a machine runs and its disk capacity for the whole billing period. Costs are informative, not an invoice.</p>
</body></html>`)
	return []byte(b.String()), nil
}
