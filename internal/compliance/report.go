// Package compliance provides host hardening posture evaluation (CIS Linux / STIG
// virtualization host subset), FIPS mode detection, and audit verification (ADR-0064).
package compliance

import (
	"bytes"
	"encoding/csv"
	"encoding/json"
	"fmt"
	"html"
	"strings"
	"time"
)

// Summary aggregates statistics across host compliance findings.
type Summary struct {
	TotalHosts    int `json:"totalHosts"`
	TotalChecks   int `json:"totalChecks"`
	PassedChecks  int `json:"passedChecks"`
	FailedChecks  int `json:"failedChecks"`
	WarningChecks int `json:"warningChecks"`
	Score         int `json:"score"` // 0-100 percentage: (passed * 100) / total
}

func (s Summary) String() string {
	hostsWord := "hosts"
	if s.TotalHosts == 1 {
		hostsWord = "host"
	}
	return fmt.Sprintf("%d %s · %d%% compliance score · %d checks: %d passed, %d failed, %d warnings",
		s.TotalHosts, hostsWord, s.Score, s.TotalChecks, s.PassedChecks, s.FailedChecks, s.WarningChecks)
}

func (s Summary) HasFailures() bool {
	return s.FailedChecks > 0
}

// Summarize aggregates counts and calculates the overall score across all hosts.
func Summarize(comp []HostCompliance) Summary {
	var s Summary
	s.TotalHosts = len(comp)
	for _, h := range comp {
		s.TotalChecks += h.TotalCount
		s.PassedChecks += h.PassedCount
		s.FailedChecks += h.FailedCount
		s.WarningChecks += h.WarningCount
	}
	if s.TotalChecks > 0 {
		s.Score = (s.PassedChecks * 100) / s.TotalChecks
	}
	return s
}

// RenderHTML produces a self-contained HTML document for the given host compliance findings,
// styled after the reports page() template and VirtoMir app.css brutalist design tokens.
func RenderHTML(comp []HostCompliance, generatedAt time.Time, by string) []byte {
	summary := Summarize(comp)

	scoreClass := "score-high"
	strokeColor := "#1b6e47"
	switch {
	case summary.Score < 70:
		scoreClass = "score-danger"
		strokeColor = "#a32626"
	case summary.Score < 90:
		scoreClass = "score-warn"
		strokeColor = "#b35e09"
	}

	passedPct := "—"
	if summary.TotalChecks > 0 {
		passedPct = fmt.Sprintf("%.0f%%", float64(summary.PassedChecks)/float64(summary.TotalChecks)*100)
	}

	hostsWord := "hosts"
	if summary.TotalHosts == 1 {
		hostsWord = "host"
	}

	summaryText := fmt.Sprintf("Generated on %s", generatedAt.UTC().Format("2006-01-02 15:04:05 UTC"))
	if strings.TrimSpace(by) != "" {
		summaryText += " by " + html.EscapeString(by)
	}

	var b strings.Builder
	b.WriteString(`<!DOCTYPE html>
<html lang="en"><head><meta charset="utf-8"><title>Host Hardening &amp; Security Compliance Report</title>
<style>
  body { margin: 0; padding: 32px; font: 13px/1.5 "IBM Plex Sans", system-ui, sans-serif; background: #f7f5f0; color: #18191c; }
  .head { border-bottom: 3px solid #c25e0e; padding-bottom: 14px; margin-bottom: 22px; display: flex; justify-content: space-between; align-items: flex-end; gap: 16px; }
  h1 { margin: 0 0 4px; font-size: 20px; text-transform: uppercase; letter-spacing: .02em; }
  .summary { color: #4a4d54; }
  .tag { font: 11px ui-monospace, monospace; border: 1px solid #18191c; padding: 3px 8px; white-space: nowrap; }
  .stat-grid { display: grid; grid-template-columns: repeat(auto-fit, minmax(180px, 1fr)); gap: 12px; margin-bottom: 24px; }
  .stat-box { border: 1px solid #bdb7ac; background: #fff; padding: 12px 14px; }
  .gauge-wrap { display: flex; align-items: center; gap: 12px; margin: 4px 0; }
  .score-ring { transform: rotate(-90deg); flex-shrink: 0; }
  .label { font: 11px ui-monospace, monospace; text-transform: uppercase; color: #4a4d54; }
  .val { font: 700 20px ui-monospace, monospace; margin: 2px 0; }
  .sub { font-size: 11px; color: #4a4d54; }
  .host-section { margin-top: 24px; margin-bottom: 24px; }
  .host-header { display: flex; justify-content: space-between; align-items: baseline; margin-bottom: 8px; border-bottom: 1px solid #bdb7ac; padding-bottom: 6px; }
  .host-title { font-size: 15px; font-weight: 700; text-transform: uppercase; }
  .host-stats { font-size: 12px; color: #4a4d54; }
  table { width: 100%; border-collapse: collapse; background: #fff; border: 1px solid #bdb7ac; }
  th { text-align: left; padding: 8px 10px; font: 11px ui-monospace, monospace; text-transform: uppercase; background: #e5e0d8; border-bottom: 1px solid #bdb7ac; }
  td { padding: 8px 10px; border-bottom: 1px solid #e5e0d8; vertical-align: top; }
  tr.fail td { background: #fdf2f2; }
  tr.warn td { background: #fdf0e1; }
  .mono { font-family: ui-monospace, "IBM Plex Mono", monospace; }
  .text-dim { color: #85827a; }
  .text-ok { color: #1b6e47; }
  .text-warn { color: #b35e09; }
  .text-danger { color: #a32626; }
  .score-high { color: #1b6e47; }
  .score-warn { color: #b35e09; }
  .score-danger { color: #a32626; }
  .badge { display: inline-block; padding: 1px 6px; font: 700 10px ui-monospace, monospace; border: 1px solid currentColor; text-transform: uppercase; }
  .badge.pass, .badge.ok     { color: #1b6e47; background: #edf7f0; border-color: #1b6e47; }
  .badge.warn                { color: #b35e09; background: #fdf6ec; border-color: #b35e09; }
  .badge.fail, .badge.danger { color: #a32626; background: #fdf2f2; border-color: #a32626; }
  .badge.error               { color: #a32626; background: #fdf2f2; border-color: #a32626; }
  .badge.cat                 { color: #4a4d54; background: #f0ede6; border-color: #bdb7ac; }
  .badge.sev-critical        { color: #a32626; background: #fde8e8; border-color: #a32626; }
  .badge.sev-high            { color: #c25e0e; background: #fef0e6; border-color: #c25e0e; }
  .badge.sev-med,
  .badge.sev-medium          { color: #8f6b00; background: #fef9e7; border-color: #8f6b00; }
  .badge.sev-low             { color: #2343b8; background: #eef2fa; border-color: #2343b8; }
  .remediation-box { background: #f0ede6; border: 1px solid #d4cebe; padding: 6px 8px; font-family: ui-monospace, "IBM Plex Mono", monospace; font-size: 11px; line-height: 1.4; white-space: pre-wrap; word-break: break-all; margin-top: 4px; user-select: all; }
  .note { color: #4a4d54; font-size: 12px; margin-top: 12px; }
  @media print {
    body { padding: 0; background: #fff; }
    .head { border-bottom: 2px solid #000; }
    .stat-box, table, tr, .remediation-box, .host-section { break-inside: avoid; page-break-inside: avoid; }
    .remediation-box { border: 1px solid #999; background: #f9f9f9; }
  }
</style></head><body>
<div class="head"><div><h1>Host Hardening &amp; Security Compliance Report</h1><div class="summary">`)
	b.WriteString(summaryText)
	b.WriteString(`</div></div><div class="tag">VirtoMir compliance</div></div>
<div class="stat-grid">
  <div class="stat-box">
    <div class="label">Cluster Compliance Score</div>
    <div class="gauge-wrap">
      <svg class="score-ring" viewBox="0 0 36 36" width="44" height="44">
        <path d="M18 2.0845 a 15.9155 15.9155 0 0 1 0 31.831 a 15.9155 15.9155 0 0 1 0 -31.831" stroke="#e5e0d8" stroke-width="3.5" fill="none"/>
        <path stroke-dasharray="`)
	fmt.Fprintf(&b, "%d, 100", summary.Score)
	b.WriteString(`" d="M18 2.0845 a 15.9155 15.9155 0 0 1 0 31.831 a 15.9155 15.9155 0 0 1 0 -31.831" stroke="`)
	b.WriteString(strokeColor)
	b.WriteString(`" stroke-width="3.5" stroke-linecap="butt" fill="none"/>
      </svg>
      <div class="val `)
	b.WriteString(scoreClass)
	fmt.Fprintf(&b, `">%d%%</div>
    </div>
    <div class="sub">%d of %d checks passed</div>
  </div>
  <div class="stat-box">
    <div class="label">Total Checks</div>
    <div class="val">%d</div>
    <div class="sub">Across %d %s</div>
  </div>
  <div class="stat-box">
    <div class="label">Passed</div>
    <div class="val text-ok">%d</div>
    <div class="sub">%s of checks</div>
  </div>
  <div class="stat-box">
    <div class="label">Failed</div>
    <div class="val text-danger">%d</div>
    <div class="sub">Requires remediation</div>
  </div>
  <div class="stat-box">
    <div class="label">Warnings</div>
    <div class="val text-warn">%d</div>
    <div class="sub">Review recommended</div>
  </div>
</div>`, summary.Score, summary.PassedChecks, summary.TotalChecks, summary.TotalChecks, summary.TotalHosts, hostsWord, summary.PassedChecks, passedPct, summary.FailedChecks, summary.WarningChecks)

	if len(comp) == 0 {
		b.WriteString(`<p class="note">No host compliance facts available.</p>`)
	} else {
		for _, h := range comp {
			hScoreClass := "score-high"
			switch {
			case h.Score < 70:
				hScoreClass = "score-danger"
			case h.Score < 90:
				hScoreClass = "score-warn"
			}

			fmt.Fprintf(&b, `<div class="host-section">
  <div class="host-header">
    <div>
      <span class="host-title">%s</span>
      <span class="mono text-dim">(%s)</span>
    </div>
    <div class="host-stats mono">
      Score: <strong class="%s">%d%%</strong> · %d passed · %d failed · %d warnings · Total: %d
    </div>
  </div>
  <table class="report-table">
    <thead>
      <tr>
        <th style="width: 100px;">Check ID</th>
        <th>Title</th>
        <th>Category</th>
        <th>Severity</th>
        <th>Status</th>
        <th>Findings &amp; Details</th>
        <th>Remediation</th>
      </tr>
    </thead>
    <tbody>`,
				html.EscapeString(h.HostName),
				html.EscapeString(h.HostID),
				hScoreClass,
				h.Score,
				h.PassedCount,
				h.FailedCount,
				h.WarningCount,
				h.TotalCount,
			)

			for _, r := range h.Results {
				rowCls := ""
				switch r.Status {
				case StatusFail, StatusError:
					rowCls = ` class="fail"`
				case StatusWarn:
					rowCls = ` class="warn"`
				}

				sevCls := "sev-" + strings.ToLower(string(r.Severity))
				statusCls := strings.ToLower(string(r.Status))

				benchmarkHTML := ""
				if strings.TrimSpace(r.Benchmark) != "" {
					benchmarkHTML = fmt.Sprintf(`<div class="sub text-dim mono" style="margin-top: 4px;">Benchmark: %s</div>`, html.EscapeString(r.Benchmark))
				}

				remediationHTML := `<span class="text-dim">—</span>`
				if strings.TrimSpace(r.Remediation) != "" {
					remediationHTML = fmt.Sprintf(`<div class="remediation-box">%s</div>`, html.EscapeString(r.Remediation))
				}

				fmt.Fprintf(&b, `<tr%s>
        <td class="mono font-bold">%s</td>
        <td><strong>%s</strong></td>
        <td><span class="badge cat">%s</span></td>
        <td><span class="badge %s">%s</span></td>
        <td><span class="badge %s">%s</span></td>
        <td>
          <div>%s</div>
          %s
        </td>
        <td>%s</td>
      </tr>`,
					rowCls,
					html.EscapeString(r.ID),
					html.EscapeString(r.Title),
					html.EscapeString(r.Category),
					sevCls,
					html.EscapeString(strings.ToUpper(string(r.Severity))),
					statusCls,
					html.EscapeString(strings.ToUpper(string(r.Status))),
					html.EscapeString(r.Detail),
					benchmarkHTML,
					remediationHTML,
				)
			}

			b.WriteString(`</tbody></table></div>`)
		}
	}

	b.WriteString(`
</body></html>`)
	return []byte(b.String())
}

// RenderCSV exports host compliance results in CSV format using encoding/csv.Writer.
// The header row matches: HostName,HostID,CheckID,Title,Category,Benchmark,Severity,Status,Detail,Remediation.
func RenderCSV(comp []HostCompliance) []byte {
	var buf bytes.Buffer
	w := csv.NewWriter(&buf)
	if err := w.Write([]string{"HostName", "HostID", "CheckID", "Title", "Category", "Benchmark", "Severity", "Status", "Detail", "Remediation"}); err != nil {
		return nil
	}
	for _, h := range comp {
		for _, r := range h.Results {
			if err := w.Write([]string{
				h.HostName,
				h.HostID,
				r.ID,
				r.Title,
				r.Category,
				r.Benchmark,
				string(r.Severity),
				string(r.Status),
				r.Detail,
				r.Remediation,
			}); err != nil {
				return nil
			}
		}
	}
	w.Flush()
	if err := w.Error(); err != nil {
		return nil
	}
	return buf.Bytes()
}

// RenderJSON serializes the compliance posture to indented JSON.
func RenderJSON(comp []HostCompliance, generatedAt time.Time) []byte {
	if comp == nil {
		comp = []HostCompliance{}
	}
	if !generatedAt.IsZero() {
		for i := range comp {
			if comp[i].EvaluatedAt.IsZero() {
				comp[i].EvaluatedAt = generatedAt
			}
		}
	}
	b, err := json.MarshalIndent(comp, "", "  ")
	if err != nil {
		return []byte("[]")
	}
	return b
}
