// Package compliance provides host hardening posture evaluation (CIS Linux / STIG
// virtualization host subset), FIPS mode detection, and audit verification (ADR-0064).
package compliance

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// CheckFunc is the signature of an individual compliance rule check.
type CheckFunc func(ctx context.Context, r *Reader) CheckResult

// AllChecks returns the ordered list of all standard compliance check functions.
func AllChecks() []CheckFunc {
	return []CheckFunc{
		CheckSSHRootLogin,
		CheckSSHEmptyPasswords,
		CheckLibvirtSecurityDriver,
		CheckHostFirewall,
		CheckUnattendedUpdates,
		CheckTimeSync,
		CheckKernelKptrRestrict,
		CheckKernelDmesgRestrict,
		CheckKernelReversePathFilter,
		CheckUnneededServices,
		CheckCoreDumpsRestricted,
	}
}

// parseSSHDirective scans /etc/ssh/sshd_config.d/*.conf then /etc/ssh/sshd_config
// and returns the value of the first matching directive encountered.
func parseSSHDirective(r *Reader, directive string) (string, bool) {
	var files []string
	if entries, err := os.ReadDir(r.path("etc/ssh/sshd_config.d")); err == nil {
		for _, e := range entries {
			if !e.IsDir() && strings.HasSuffix(e.Name(), ".conf") {
				files = append(files, filepath.Join("etc/ssh/sshd_config.d", e.Name()))
			}
		}
	}
	files = append(files, "etc/ssh/sshd_config")

	for _, rel := range files {
		data, err := os.ReadFile(r.path(rel))
		if err != nil {
			continue
		}
		for _, line := range strings.Split(string(data), "\n") {
			line = strings.TrimSpace(line)
			if line == "" || strings.HasPrefix(line, "#") {
				continue
			}
			fields := strings.Fields(line)
			if len(fields) >= 2 && strings.EqualFold(fields[0], directive) {
				return fields[1], true
			}
		}
	}
	return "", false
}

// CheckSSHRootLogin: CIS 5.2.5. Reads /etc/ssh/sshd_config and /etc/ssh/sshd_config.d/*.conf.
// Pass if 'no' or 'prohibit-password'; Fail if 'yes' or missing.
func CheckSSHRootLogin(ctx context.Context, r *Reader) CheckResult {
	const (
		id          = "cis-5.2.5-ssh-root-login"
		title       = "SSH Root Login"
		cat         = "SSH"
		sev         = SeverityHigh
		remediation = "Set PermitRootLogin prohibit-password in /etc/ssh/sshd_config and reload sshd via systemctl reload ssh."
		benchmark   = "CIS Linux 2.0 / STIG"
	)

	val, found := parseSSHDirective(r, "PermitRootLogin")
	if !found {
		return CheckResult{
			ID:          id,
			Title:       title,
			Category:    cat,
			Severity:    sev,
			Status:      StatusFail,
			Detail:      "PermitRootLogin directive is missing in sshd configuration",
			Remediation: remediation,
			Benchmark:   benchmark,
		}
	}

	valLower := strings.ToLower(val)
	if valLower == "no" || valLower == "prohibit-password" || valLower == "without-password" {
		return CheckResult{
			ID:          id,
			Title:       title,
			Category:    cat,
			Severity:    sev,
			Status:      StatusPass,
			Detail:      fmt.Sprintf("PermitRootLogin is configured to %q", val),
			Remediation: remediation,
			Benchmark:   benchmark,
		}
	}

	return CheckResult{
		ID:          id,
		Title:       title,
		Category:    cat,
		Severity:    sev,
		Status:      StatusFail,
		Detail:      fmt.Sprintf("PermitRootLogin is configured to %q (expected 'no' or 'prohibit-password')", val),
		Remediation: remediation,
		Benchmark:   benchmark,
	}
}

// CheckSSHEmptyPasswords: CIS 5.2.11. Reads sshd configuration.
// Pass if 'no'; Fail if 'yes'.
func CheckSSHEmptyPasswords(ctx context.Context, r *Reader) CheckResult {
	const (
		id          = "cis-5.2.11-ssh-empty-passwords"
		title       = "SSH Empty Passwords"
		cat         = "SSH"
		sev         = SeverityHigh
		remediation = "Set PermitEmptyPasswords no in /etc/ssh/sshd_config."
		benchmark   = "CIS Linux 2.0 / STIG"
	)

	val, found := parseSSHDirective(r, "PermitEmptyPasswords")
	if !found {
		return CheckResult{
			ID:          id,
			Title:       title,
			Category:    cat,
			Severity:    sev,
			Status:      StatusPass,
			Detail:      "PermitEmptyPasswords is not configured (defaults to 'no')",
			Remediation: remediation,
			Benchmark:   benchmark,
		}
	}

	if strings.EqualFold(val, "no") {
		return CheckResult{
			ID:          id,
			Title:       title,
			Category:    cat,
			Severity:    sev,
			Status:      StatusPass,
			Detail:      "PermitEmptyPasswords is set to 'no'",
			Remediation: remediation,
			Benchmark:   benchmark,
		}
	}

	return CheckResult{
		ID:          id,
		Title:       title,
		Category:    cat,
		Severity:    sev,
		Status:      StatusFail,
		Detail:      fmt.Sprintf("PermitEmptyPasswords is set to %q (expected 'no')", val),
		Remediation: remediation,
		Benchmark:   benchmark,
	}
}

// CheckLibvirtSecurityDriver: CIS/STIG sVirt requirement for virtualization hosts.
//
// Pass when MAC isolation is actually active:
//   - SELinux is enforcing and every running qemu-system process runs with an
//     svirt_t / svirt_tcg_t context, or
//   - AppArmor is enabled and every running qemu-system process is confined by a
//     'libvirt-<uuid> (enforce)' profile, or
//   - no VM is running while AppArmor (or enforcing SELinux) is enabled and
//     qemu.conf does not disable sVirt.
//
// Fail when security_driver = "none", no MAC system is enabled, or running qemu
// processes run unconfined / in complain mode (the offenders are reported in Detail).
// All facts are read from files under Reader.Root; nothing is probed via external
// commands (aa-status, getenforce, sysctl may be absent on minimal hosts).
func CheckLibvirtSecurityDriver(ctx context.Context, r *Reader) CheckResult {
	const (
		id          = "stig-svirt-mac-isolation"
		title       = "sVirt MAC Isolation (AppArmor / SELinux)"
		cat         = "Virtualization"
		sev         = SeverityCritical
		remediation = `Configure sVirt MAC isolation: enable AppArmor or SELinux and set security_driver = "apparmor" in /etc/libvirt/qemu.conf, then restart libvirtd.`
		benchmark   = "CIS Linux 2.0 / STIG"
	)

	mk := func(status Status, detail string) CheckResult {
		return CheckResult{
			ID:          id,
			Title:       title,
			Category:    cat,
			Severity:    sev,
			Status:      status,
			Detail:      detail,
			Remediation: remediation,
			Benchmark:   benchmark,
		}
	}

	driverVal, driverFound := parseSecurityDriver(r)
	if driverFound && strings.Contains(driverVal, "none") {
		return mk(StatusFail, "security_driver is explicitly set to 'none' in /etc/libvirt/qemu.conf (sVirt isolation disabled)")
	}
	if driverFound && !strings.Contains(driverVal, "apparmor") && !strings.Contains(driverVal, "selinux") {
		return mk(StatusFail, fmt.Sprintf("security_driver %q in /etc/libvirt/qemu.conf does not enable AppArmor or SELinux", driverVal))
	}

	selinuxEnforcing := r.read("sys/fs/selinux/enforce") == "1"
	apparmorEnabled, apparmorDesc := appArmorEnabled(r)

	// If qemu.conf pins one backend, that backend must actually be active (the
	// libvirt default is the enabled MAC system when no security_driver line exists).
	if driverFound {
		if strings.Contains(driverVal, "apparmor") && !apparmorEnabled {
			return mk(StatusFail, "AppArmor configured in qemu.conf but kernel AppArmor support is not detected ("+apparmorDesc+")")
		}
		if strings.Contains(driverVal, "selinux") && !selinuxEnforcing {
			return mk(StatusFail, "SELinux configured in qemu.conf but kernel SELinux is not in enforcing mode (/sys/fs/selinux/enforce)")
		}
	}

	if !apparmorEnabled && !selinuxEnforcing {
		return mk(StatusFail, "No MAC isolation system is enabled (AppArmor support not detected and SELinux not enforcing)")
	}

	backend := "apparmor"
	if driverFound {
		if strings.Contains(driverVal, "apparmor") {
			backend = "apparmor"
		} else {
			backend = "selinux"
		}
	} else if selinuxEnforcing && !apparmorEnabled {
		backend = "selinux"
	}

	procs := qemuProcesses(r)
	if len(procs) == 0 {
		name := "AppArmor"
		if backend == "selinux" {
			name = "SELinux enforcing"
		}
		return mk(StatusPass, "sVirt ready: "+name+" is active and security_driver is not disabled; no VM is currently running")
	}

	var offenders []string
	for _, p := range procs {
		label := p.label
		switch {
		case label == "" || label == "unconfined" || strings.HasPrefix(label, "unconfined "):
			offenders = append(offenders, fmt.Sprintf("process %s runs unconfined (attr/current = %q)", p.pid, label))
		case strings.Contains(label, "(complain)"):
			offenders = append(offenders, fmt.Sprintf("process %s is in complain mode (attr/current = %q)", p.pid, label))
		case backend == "apparmor":
			if !libvirtAppArmorConfined(label) {
				offenders = append(offenders, fmt.Sprintf("process %s is not confined by a libvirt profile (attr/current = %q)", p.pid, label))
			}
		default:
			if !strings.Contains(label, "svirt_t") {
				offenders = append(offenders, fmt.Sprintf("process %s has no sVirt SELinux context (attr/current = %q)", p.pid, label))
			}
		}
	}

	if len(offenders) > 0 {
		return mk(StatusFail, "sVirt MAC isolation not fully active: "+strings.Join(offenders, "; "))
	}

	if backend == "apparmor" {
		return mk(StatusPass, fmt.Sprintf("sVirt MAC isolation active: AppArmor enabled (%s) and all %d running qemu processes confined by libvirt-<uuid> profiles in enforce mode", apparmorDesc, len(procs)))
	}
	return mk(StatusPass, fmt.Sprintf("sVirt MAC isolation active: SELinux enforcing and all %d running qemu processes run with an svirt_t/svirt_tcg_t context", len(procs)))
}

// parseSecurityDriver reads security_driver from /etc/libvirt/qemu.conf.
func parseSecurityDriver(r *Reader) (val string, found bool) {
	qemuConf := r.read("etc/libvirt/qemu.conf")
	if qemuConf == "" {
		return "", false
	}
	for _, line := range strings.Split(qemuConf, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		parts := strings.SplitN(line, "=", 2)
		if len(parts) == 2 && strings.TrimSpace(parts[0]) == "security_driver" {
			v := strings.TrimSpace(parts[1])
			if idx := strings.Index(v, "#"); idx != -1 {
				v = strings.TrimSpace(v[:idx])
			}
			return strings.ToLower(v), true
		}
	}
	return "", false
}

// appArmorEnabled reports whether kernel AppArmor support is active, plus a short
// human-readable description of the evidence. Only files under Reader.Root are
// consulted: /sys/module/apparmor/parameter/enabled ("Y" = module enabled) and the
// /sys/kernel/security/apparmor securityfs interface (aa-status may not be installed).
func appArmorEnabled(r *Reader) (ok bool, desc string) {
	switch param := r.read("sys/module/apparmor/parameter/enabled"); {
	case param == "Y" || param == "y":
		return true, "kernel AppArmor module enabled"
	case param != "":
		return false, "kernel AppArmor module disabled"
	}
	if r.exists("sys/kernel/security/apparmor") {
		if profiles := r.read("sys/kernel/security/apparmor/profiles"); profiles != "" {
			return true, fmt.Sprintf("AppArmor securityfs interface present with %d loaded profile entries", len(strings.Fields(profiles)))
		}
		return true, "AppArmor securityfs interface present"
	}
	return false, "kernel AppArmor support not detected"
}

// qemuProcess is a running qemu-system process and its MAC label.
type qemuProcess struct {
	pid   string
	label string // /proc/<pid>/attr/current ("" if unreadable)
}

// qemuProcesses lists running qemu-system processes by scanning /proc numeric
// directories whose cmdline mentions qemu-system; no external process listing
// helper (ps, pgrep) is required. label is /proc/<pid>/attr/current.
func qemuProcesses(r *Reader) []qemuProcess {
	entries, err := os.ReadDir(r.path("proc"))
	if err != nil {
		return nil
	}
	var procs []qemuProcess
	for _, e := range entries {
		if !e.IsDir() || !isPid(e.Name()) {
			continue
		}
		cmd, err := os.ReadFile(r.path(filepath.Join("proc", e.Name(), "cmdline")))
		if err != nil || !strings.Contains(string(cmd), "qemu-system") {
			continue
		}
		procs = append(procs, qemuProcess{
			pid:   e.Name(),
			label: r.read(filepath.Join("proc", e.Name(), "attr", "current")),
		})
	}
	return procs
}

// isPid reports whether name consists of digits only (a /proc process directory).
func isPid(name string) bool {
	if name == "" {
		return false
	}
	for _, c := range name {
		if c < '0' || c > '9' {
			return false
		}
	}
	return true
}

// libvirtAppArmorConfined reports whether a /proc/<pid>/attr/current value shows a
// process confined by a per-VM libvirt profile ('libvirt-<uuid> (enforce)'). A bare
// 'libvirt-<uuid>' (without mode annotation, older kernels) also counts as confined;
// complain mode does not.
func libvirtAppArmorConfined(label string) bool {
	label = strings.TrimSpace(label)
	if !strings.HasPrefix(label, "libvirt-") {
		return false
	}
	idx := strings.Index(label, " (")
	if idx == -1 {
		return true
	}
	return strings.Contains(label[idx:], "(enforce)")
}

// CheckHostFirewall: CIS 3.5.1. Checks /proc/net/ip_tables_names or executes
// 'nft list ruleset' / 'systemctl is-active nftables firewalld ufw'.
// Pass if active rules or firewall daemon found.
func CheckHostFirewall(ctx context.Context, r *Reader) CheckResult {
	const (
		id          = "cis-3.5.1-host-firewall"
		title       = "Host Firewall"
		cat         = "Firewall"
		sev         = SeverityHigh
		remediation = "Enable host firewall: ufw enable or systemctl enable --now nftables."
		benchmark   = "CIS Linux 2.0 / STIG"
	)

	// 1. Check procfs iptables tables
	iptables := r.read("proc/net/ip_tables_names")
	if strings.TrimSpace(iptables) != "" {
		names := strings.Join(strings.Fields(iptables), ", ")
		return CheckResult{
			ID:          id,
			Title:       title,
			Category:    cat,
			Severity:    sev,
			Status:      StatusPass,
			Detail:      fmt.Sprintf("Active iptables packet filtering tables detected: %s", names),
			Remediation: remediation,
			Benchmark:   benchmark,
		}
	}

	ip6tables := r.read("proc/net/ip6_tables_names")
	if strings.TrimSpace(ip6tables) != "" {
		names := strings.Join(strings.Fields(ip6tables), ", ")
		return CheckResult{
			ID:          id,
			Title:       title,
			Category:    cat,
			Severity:    sev,
			Status:      StatusPass,
			Detail:      fmt.Sprintf("Active ip6tables packet filtering tables detected: %s", names),
			Remediation: remediation,
			Benchmark:   benchmark,
		}
	}

	// 2. Unit files in systemd directory
	firewallUnits := []string{"nftables.service", "ufw.service", "firewalld.service"}
	for _, unit := range firewallUnits {
		if r.exists("etc/systemd/system/multi-user.target.wants/"+unit) ||
			r.exists("etc/systemd/system/"+unit) {
			return CheckResult{
				ID:          id,
				Title:       title,
				Category:    cat,
				Severity:    sev,
				Status:      StatusPass,
				Detail:      fmt.Sprintf("Host firewall service unit enabled: %s", unit),
				Remediation: remediation,
				Benchmark:   benchmark,
			}
		}
	}

	// 3. Command: nft list ruleset
	if out, err := r.run(ctx, "nft", "list", "ruleset"); err == nil && len(strings.TrimSpace(string(out))) > 0 {
		return CheckResult{
			ID:          id,
			Title:       title,
			Category:    cat,
			Severity:    sev,
			Status:      StatusPass,
			Detail:      "Active nftables ruleset detected",
			Remediation: remediation,
			Benchmark:   benchmark,
		}
	}

	// 4. Command: systemctl is-active nftables firewalld ufw
	for _, svc := range []string{"nftables", "firewalld", "ufw"} {
		if out, err := r.run(ctx, "systemctl", "is-active", svc); err == nil && strings.TrimSpace(string(out)) == "active" {
			return CheckResult{
				ID:          id,
				Title:       title,
				Category:    cat,
				Severity:    sev,
				Status:      StatusPass,
				Detail:      fmt.Sprintf("Firewall daemon %s is active", svc),
				Remediation: remediation,
				Benchmark:   benchmark,
			}
		}
	}

	return CheckResult{
		ID:          id,
		Title:       title,
		Category:    cat,
		Severity:    sev,
		Status:      StatusFail,
		Detail:      "No active firewall rules or active firewall daemon (nftables, firewalld, ufw) detected",
		Remediation: remediation,
		Benchmark:   benchmark,
	}
}

// CheckUnattendedUpdates: CIS 1.8. Reads /etc/apt/apt.conf.d/20auto-upgrades or
// 50unattended-upgrades (verifies APT::Periodic::Unattended-Upgrade "1";) or /etc/dnf/automatic.conf (apply_updates = yes).
func CheckUnattendedUpdates(ctx context.Context, r *Reader) CheckResult {
	const (
		id          = "cis-1.8-unattended-updates"
		title       = "Unattended Security Updates"
		cat         = "System"
		sev         = SeverityMedium
		remediation = "Install and enable unattended-upgrades: apt-get install unattended-upgrades && dpkg-reconfigure -plow unattended-upgrades."
		benchmark   = "CIS Linux 2.0 / STIG"
	)

	aptFiles := []string{
		"etc/apt/apt.conf.d/20auto-upgrades",
		"etc/apt/apt.conf.d/50unattended-upgrades",
	}
	if entries, err := os.ReadDir(r.path("etc/apt/apt.conf.d")); err == nil {
		for _, e := range entries {
			if !e.IsDir() {
				aptFiles = append(aptFiles, filepath.Join("etc/apt/apt.conf.d", e.Name()))
			}
		}
	}

	for _, cand := range aptFiles {
		content := r.read(cand)
		if content != "" && strings.Contains(content, `APT::Periodic::Unattended-Upgrade "1";`) {
			return CheckResult{
				ID:          id,
				Title:       title,
				Category:    cat,
				Severity:    sev,
				Status:      StatusPass,
				Detail:      fmt.Sprintf("Automatic security updates enabled via %s", cand),
				Remediation: remediation,
				Benchmark:   benchmark,
			}
		}
	}

	dnfContent := r.read("etc/dnf/automatic.conf")
	if dnfContent != "" {
		for _, line := range strings.Split(dnfContent, "\n") {
			line = strings.TrimSpace(line)
			if strings.HasPrefix(line, "#") || strings.HasPrefix(line, ";") {
				continue
			}
			parts := strings.SplitN(line, "=", 2)
			if len(parts) == 2 && strings.TrimSpace(strings.ToLower(parts[0])) == "apply_updates" {
				if strings.TrimSpace(strings.ToLower(parts[1])) == "yes" {
					return CheckResult{
						ID:          id,
						Title:       title,
						Category:    cat,
						Severity:    sev,
						Status:      StatusPass,
						Detail:      "Automatic updates enabled in /etc/dnf/automatic.conf (apply_updates = yes)",
						Remediation: remediation,
						Benchmark:   benchmark,
					}
				}
			}
		}
	}

	return CheckResult{
		ID:          id,
		Title:       title,
		Category:    cat,
		Severity:    sev,
		Status:      StatusFail,
		Detail:      "Automatic security updates are not configured in APT or DNF",
		Remediation: remediation,
		Benchmark:   benchmark,
	}
}

// CheckTimeSync: CIS 2.2.1. Checks active state of systemd-timesyncd, chrony, or ntp
// via unit files in /etc/systemd/system or timedatectl.
func CheckTimeSync(ctx context.Context, r *Reader) CheckResult {
	const (
		id          = "cis-2.2.1-time-synchronization"
		title       = "Network Time Synchronization"
		cat         = "System"
		sev         = SeverityMedium
		remediation = "Enable network time synchronization: systemctl enable --now systemd-timesyncd or chrony."
		benchmark   = "CIS Linux 2.0 / STIG"
	)

	timeUnits := []string{"systemd-timesyncd.service", "chrony.service", "chronyd.service", "ntp.service", "ntpd.service"}
	prefixes := []string{
		"etc/systemd/system/multi-user.target.wants/",
		"etc/systemd/system/sysinit.target.wants/",
		"etc/systemd/system/",
		"usr/lib/systemd/system/multi-user.target.wants/",
		"lib/systemd/system/sysinit.target.wants/",
	}
	for _, pfx := range prefixes {
		for _, u := range timeUnits {
			if r.exists(pfx + u) {
				return CheckResult{
					ID:          id,
					Title:       title,
					Category:    cat,
					Severity:    sev,
					Status:      StatusPass,
					Detail:      fmt.Sprintf("Time synchronization service unit enabled: %s", u),
					Remediation: remediation,
					Benchmark:   benchmark,
				}
			}
		}
	}

	if out, err := r.run(ctx, "timedatectl", "show"); err == nil {
		outStr := string(out)
		if strings.Contains(outStr, "NTP=yes") || strings.Contains(outStr, "NTPSynchronized=yes") {
			return CheckResult{
				ID:          id,
				Title:       title,
				Category:    cat,
				Severity:    sev,
				Status:      StatusPass,
				Detail:      "Network time synchronization active via timedatectl (NTP=yes)",
				Remediation: remediation,
				Benchmark:   benchmark,
			}
		}
	}

	for _, svc := range []string{"systemd-timesyncd", "chrony", "chronyd", "ntp", "ntpd"} {
		if out, err := r.run(ctx, "systemctl", "is-active", svc); err == nil && strings.TrimSpace(string(out)) == "active" {
			return CheckResult{
				ID:          id,
				Title:       title,
				Category:    cat,
				Severity:    sev,
				Status:      StatusPass,
				Detail:      fmt.Sprintf("Time synchronization daemon %s is active", svc),
				Remediation: remediation,
				Benchmark:   benchmark,
			}
		}
	}

	return CheckResult{
		ID:          id,
		Title:       title,
		Category:    cat,
		Severity:    sev,
		Status:      StatusFail,
		Detail:      "Network time synchronization (systemd-timesyncd, chrony, ntp) is not active or enabled",
		Remediation: remediation,
		Benchmark:   benchmark,
	}
}

// CheckKernelKptrRestrict: CIS 1.5.1. Reads /proc/sys/kernel/kptr_restrict. Pass if >= 1.
func CheckKernelKptrRestrict(ctx context.Context, r *Reader) CheckResult {
	const (
		id          = "cis-1.5.1-kernel-kptr-restrict"
		title       = "Kernel Pointer Restriction (kptr_restrict)"
		cat         = "Kernel"
		sev         = SeverityHigh
		remediation = "Set kernel.kptr_restrict = 1 in /etc/sysctl.d/99-virto-hardening.conf and apply with sysctl --system."
		benchmark   = "CIS Linux 2.0 / STIG"
	)

	v, ok := r.readInt("proc/sys/kernel/kptr_restrict")
	if ok && v >= 1 {
		return CheckResult{
			ID:          id,
			Title:       title,
			Category:    cat,
			Severity:    sev,
			Status:      StatusPass,
			Detail:      fmt.Sprintf("kernel.kptr_restrict is %d (>= 1)", v),
			Remediation: remediation,
			Benchmark:   benchmark,
		}
	}

	raw := r.read("proc/sys/kernel/kptr_restrict")
	if raw == "" {
		raw = "missing"
	}
	return CheckResult{
		ID:          id,
		Title:       title,
		Category:    cat,
		Severity:    sev,
		Status:      StatusFail,
		Detail:      fmt.Sprintf("kernel.kptr_restrict is %s (expected >= 1)", raw),
		Remediation: remediation,
		Benchmark:   benchmark,
	}
}

// CheckKernelDmesgRestrict: CIS 1.5.2. Reads /proc/sys/kernel/dmesg_restrict. Pass if == 1.
func CheckKernelDmesgRestrict(ctx context.Context, r *Reader) CheckResult {
	const (
		id          = "cis-1.5.2-kernel-dmesg-restrict"
		title       = "Kernel dmesg Restriction (dmesg_restrict)"
		cat         = "Kernel"
		sev         = SeverityMedium
		remediation = "Set kernel.dmesg_restrict = 1 in /etc/sysctl.d/99-virto-hardening.conf."
		benchmark   = "CIS Linux 2.0 / STIG"
	)

	v, ok := r.readInt("proc/sys/kernel/dmesg_restrict")
	if ok && v == 1 {
		return CheckResult{
			ID:          id,
			Title:       title,
			Category:    cat,
			Severity:    sev,
			Status:      StatusPass,
			Detail:      "kernel.dmesg_restrict is 1",
			Remediation: remediation,
			Benchmark:   benchmark,
		}
	}

	raw := r.read("proc/sys/kernel/dmesg_restrict")
	if raw == "" {
		raw = "missing"
	}
	return CheckResult{
		ID:          id,
		Title:       title,
		Category:    cat,
		Severity:    sev,
		Status:      StatusFail,
		Detail:      fmt.Sprintf("kernel.dmesg_restrict is %s (expected 1)", raw),
		Remediation: remediation,
		Benchmark:   benchmark,
	}
}

// CheckKernelReversePathFilter: CIS 3.2.1. Reads /proc/sys/net/ipv4/conf/all/rp_filter. Pass if == 1.
func CheckKernelReversePathFilter(ctx context.Context, r *Reader) CheckResult {
	const (
		id          = "cis-3.2.1-kernel-rp-filter"
		title       = "Reverse Path Filtering (rp_filter)"
		cat         = "Kernel"
		sev         = SeverityMedium
		remediation = "Set net.ipv4.conf.all.rp_filter = 1 in /etc/sysctl.d/99-virto-hardening.conf."
		benchmark   = "CIS Linux 2.0 / STIG"
	)

	v, ok := r.readInt("proc/sys/net/ipv4/conf/all/rp_filter")
	if ok && v == 1 {
		return CheckResult{
			ID:          id,
			Title:       title,
			Category:    cat,
			Severity:    sev,
			Status:      StatusPass,
			Detail:      "net.ipv4.conf.all.rp_filter is 1",
			Remediation: remediation,
			Benchmark:   benchmark,
		}
	}

	raw := r.read("proc/sys/net/ipv4/conf/all/rp_filter")
	if raw == "" {
		raw = "missing"
	}
	return CheckResult{
		ID:          id,
		Title:       title,
		Category:    cat,
		Severity:    sev,
		Status:      StatusFail,
		Detail:      fmt.Sprintf("net.ipv4.conf.all.rp_filter is %s (expected 1)", raw),
		Remediation: remediation,
		Benchmark:   benchmark,
	}
}

// CheckUnneededServices: CIS 2.1. Probes for legacy insecure services (telnet, rsh, rlogin, tftp, vsftpd)
// in /etc/inetd.conf, /etc/xinetd.d/, or systemctl.
func CheckUnneededServices(ctx context.Context, r *Reader) CheckResult {
	const (
		id          = "cis-2.1-unneeded-services"
		title       = "Legacy Insecure Services"
		cat         = "System"
		sev         = SeverityHigh
		remediation = "Uninstall legacy insecure services: apt purge telnetd rsh-server tftpd."
		benchmark   = "CIS Linux 2.0 / STIG"
	)

	legacyNames := []string{"telnet", "rsh", "rlogin", "tftp", "vsftpd"}
	var found []string

	// 1. /etc/inetd.conf
	inetd := r.read("etc/inetd.conf")
	if inetd != "" {
		for _, line := range strings.Split(inetd, "\n") {
			line = strings.TrimSpace(line)
			if line == "" || strings.HasPrefix(line, "#") {
				continue
			}
			lower := strings.ToLower(line)
			for _, name := range legacyNames {
				if strings.Contains(lower, name) {
					found = append(found, fmt.Sprintf("%s (inetd)", name))
				}
			}
		}
	}

	// 2. /etc/xinetd.d/
	if entries, err := os.ReadDir(r.path("etc/xinetd.d")); err == nil {
		for _, e := range entries {
			if e.IsDir() {
				continue
			}
			nameLower := strings.ToLower(e.Name())
			content := strings.ToLower(r.read(filepath.Join("etc/xinetd.d", e.Name())))
			disabled := strings.Contains(content, "disable = yes") || strings.Contains(content, "disable=yes")
			if !disabled {
				for _, legacy := range legacyNames {
					if strings.Contains(nameLower, legacy) || strings.Contains(content, legacy) {
						found = append(found, fmt.Sprintf("%s (xinetd)", legacy))
						break
					}
				}
			}
		}
	}

	// 3. Systemd units in multi-user.target.wants
	systemdServices := []string{
		"telnet.service", "telnetd.service", "rsh.service", "rlogind.service",
		"tftp.service", "tftpd.service", "tftpd-hpa.service", "vsftpd.service",
	}
	for _, svc := range systemdServices {
		if r.exists("etc/systemd/system/multi-user.target.wants/" + svc) {
			found = append(found, fmt.Sprintf("%s (systemd enabled)", svc))
		}
	}

	// 4. systemctl is-active
	for _, svc := range systemdServices {
		if out, err := r.run(ctx, "systemctl", "is-active", svc); err == nil && strings.TrimSpace(string(out)) == "active" {
			found = append(found, fmt.Sprintf("%s (active daemon)", svc))
		}
	}

	if len(found) > 0 {
		return CheckResult{
			ID:          id,
			Title:       title,
			Category:    cat,
			Severity:    sev,
			Status:      StatusFail,
			Detail:      fmt.Sprintf("Legacy insecure service(s) configured or active: %s", strings.Join(found, ", ")),
			Remediation: remediation,
			Benchmark:   benchmark,
		}
	}

	return CheckResult{
		ID:          id,
		Title:       title,
		Category:    cat,
		Severity:    sev,
		Status:      StatusPass,
		Detail:      "No legacy insecure services (telnet, rsh, rlogin, tftp, vsftpd) detected",
		Remediation: remediation,
		Benchmark:   benchmark,
	}
}

// CheckCoreDumpsRestricted: CIS 1.5.4. Reads /proc/sys/fs/suid_dumpable (must be 0 or 2)
// and checks /etc/security/limits.conf for '* hard core 0'.
func CheckCoreDumpsRestricted(ctx context.Context, r *Reader) CheckResult {
	const (
		id          = "cis-1.5.4-core-dumps-restricted"
		title       = "Core Dumps Restricted"
		cat         = "System"
		sev         = SeverityMedium
		remediation = "Set fs.suid_dumpable = 0 in /etc/sysctl.d/99-virto-hardening.conf and add * hard core 0 to /etc/security/limits.conf."
		benchmark   = "CIS Linux 2.0 / STIG"
	)

	v, ok := r.readInt("proc/sys/fs/suid_dumpable")
	suidDumpableOk := ok && (v == 0 || v == 2)

	limitsCandidates := []string{"etc/security/limits.conf"}
	if entries, err := os.ReadDir(r.path("etc/security/limits.d")); err == nil {
		for _, e := range entries {
			if !e.IsDir() && strings.HasSuffix(e.Name(), ".conf") {
				limitsCandidates = append(limitsCandidates, filepath.Join("etc/security/limits.d", e.Name()))
			}
		}
	}

	limitsOk := false
	for _, cand := range limitsCandidates {
		content := r.read(cand)
		if content == "" {
			continue
		}
		for _, line := range strings.Split(content, "\n") {
			line = strings.TrimSpace(line)
			if line == "" || strings.HasPrefix(line, "#") {
				continue
			}
			f := strings.Fields(line)
			if len(f) >= 4 && f[0] == "*" && strings.EqualFold(f[1], "hard") && strings.EqualFold(f[2], "core") && f[3] == "0" {
				limitsOk = true
				break
			}
		}
		if limitsOk {
			break
		}
	}

	if suidDumpableOk && limitsOk {
		return CheckResult{
			ID:          id,
			Title:       title,
			Category:    cat,
			Severity:    sev,
			Status:      StatusPass,
			Detail:      fmt.Sprintf("Core dumps restricted: fs.suid_dumpable=%d and '* hard core 0' configured in limits", v),
			Remediation: remediation,
			Benchmark:   benchmark,
		}
	}

	var reasons []string
	if !suidDumpableOk {
		raw := r.read("proc/sys/fs/suid_dumpable")
		if raw == "" {
			raw = "missing"
		}
		reasons = append(reasons, fmt.Sprintf("fs.suid_dumpable is %s (must be 0 or 2)", raw))
	}
	if !limitsOk {
		reasons = append(reasons, "'* hard core 0' is missing in /etc/security/limits.conf")
	}

	return CheckResult{
		ID:          id,
		Title:       title,
		Category:    cat,
		Severity:    sev,
		Status:      StatusFail,
		Detail:      strings.Join(reasons, "; "),
		Remediation: remediation,
		Benchmark:   benchmark,
	}
}
