package compliance

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// tree creates a mock filesystem root populated with the specified files.
func tree(t *testing.T, files map[string]string) string {
	t.Helper()
	root := t.TempDir()
	for p, c := range files {
		clean := strings.TrimPrefix(filepath.ToSlash(p), "/")
		full := filepath.Join(root, filepath.FromSlash(clean))
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(c+"\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return root
}

func hardenedFiles() map[string]string {
	return map[string]string{
		"etc/ssh/sshd_config": strings.Join([]string{
			"Protocol 2",
			"PermitRootLogin prohibit-password",
			"PermitEmptyPasswords no",
		}, "\n"),
		"etc/libvirt/qemu.conf": `security_driver = "apparmor"`,
		"sys/kernel/security/apparmor/profiles": "default (enforce)",
		"proc/net/ip_tables_names": "filter\nmangle\nnat",
		"etc/apt/apt.conf.d/20auto-upgrades": `APT::Periodic::Update-Package-Lists "1";` + "\n" + `APT::Periodic::Unattended-Upgrade "1";`,
		"etc/systemd/system/multi-user.target.wants/systemd-timesyncd.service": "# timesyncd enabled",
		"proc/sys/kernel/kptr_restrict": "1",
		"proc/sys/kernel/dmesg_restrict": "1",
		"proc/sys/net/ipv4/conf/all/rp_filter": "1",
		"etc/inetd.conf": "# all legacy services disabled",
		"proc/sys/fs/suid_dumpable": "0",
		"etc/security/limits.conf": "* hard core 0",
	}
}

// TestHardenedHostFixture: mock filesystem tree with all CIS recommendations satisfied;
// assert 100% score and all checks pass.
func TestHardenedHostFixture(t *testing.T) {
	ctx := context.Background()
	root := tree(t, hardenedFiles())
	reader := NewReader(root)
	scanner := NewScanner(reader)

	comp := scanner.Scan(ctx, "host-cis-1", "hardened-node")

	if comp.HostID != "host-cis-1" {
		t.Errorf("HostID = %q, want 'host-cis-1'", comp.HostID)
	}
	if comp.HostName != "hardened-node" {
		t.Errorf("HostName = %q, want 'hardened-node'", comp.HostName)
	}
	if comp.Score != 100 {
		t.Errorf("Score = %d, want 100", comp.Score)
	}
	if comp.PassedCount != 11 {
		t.Errorf("PassedCount = %d, want 11", comp.PassedCount)
	}
	if comp.FailedCount != 0 {
		t.Errorf("FailedCount = %d, want 0", comp.FailedCount)
	}
	if comp.WarningCount != 0 {
		t.Errorf("WarningCount = %d, want 0", comp.WarningCount)
	}
	if comp.TotalCount != 11 {
		t.Errorf("TotalCount = %d, want 11", comp.TotalCount)
	}

	for _, res := range comp.Results {
		if res.Status != StatusPass {
			t.Errorf("check %s (%s) expected pass, got %s: %s", res.ID, res.Title, res.Status, res.Detail)
		}
		if res.Benchmark != "CIS Linux 2.0 / STIG" {
			t.Errorf("check %s benchmark = %q, want 'CIS Linux 2.0 / STIG'", res.ID, res.Benchmark)
		}
		if res.Remediation == "" {
			t.Errorf("check %s has empty remediation", res.ID)
		}
	}
}

// TestNonHardenedHostFixture: mock filesystem with PermitRootLogin yes, kptr_restrict=0,
// sVirt none, no firewall; assert checks fail with specific remediation text and Score reflects failures.
func TestNonHardenedHostFixture(t *testing.T) {
	ctx := context.Background()
	files := map[string]string{
		"etc/ssh/sshd_config": "PermitRootLogin yes\nPermitEmptyPasswords no",
		"proc/sys/kernel/kptr_restrict": "0",
		"etc/libvirt/qemu.conf": `security_driver = "none"`,
		// No firewall: proc/net/ip_tables_names is omitted
		"etc/apt/apt.conf.d/20auto-upgrades": `APT::Periodic::Unattended-Upgrade "1";`,
		"etc/systemd/system/multi-user.target.wants/systemd-timesyncd.service": "# unit",
		"proc/sys/kernel/dmesg_restrict": "1",
		"proc/sys/net/ipv4/conf/all/rp_filter": "1",
		"etc/inetd.conf": "# clean",
		"proc/sys/fs/suid_dumpable": "0",
		"etc/security/limits.conf": "* hard core 0",
	}

	root := tree(t, files)
	reader := NewReader(root)
	// Ensure external commands (nft, systemctl) return non-zero/error
	reader.Command = func(ctx context.Context, name string, args ...string) ([]byte, error) {
		return nil, fmt.Errorf("command %s disabled in mock", name)
	}

	scanner := NewScanner(reader)
	comp := scanner.Scan(ctx, "host-bad-1", "unhardened-node")

	if comp.Score >= 100 {
		t.Errorf("Score = %d, expected < 100 due to failures", comp.Score)
	}
	if comp.FailedCount < 4 {
		t.Errorf("FailedCount = %d, expected at least 4 failures", comp.FailedCount)
	}

	failedMap := make(map[string]CheckResult)
	for _, res := range comp.Results {
		if res.Status == StatusFail {
			failedMap[res.ID] = res
		}
	}

	// 1. Assert SSHRootLogin failure & remediation
	sshRes, ok := failedMap["cis-5.2.5-ssh-root-login"]
	if !ok {
		t.Errorf("expected cis-5.2.5-ssh-root-login to fail")
	} else {
		expectedRem := "Set PermitRootLogin prohibit-password in /etc/ssh/sshd_config and reload sshd via systemctl reload ssh."
		if sshRes.Remediation != expectedRem {
			t.Errorf("SSHRootLogin remediation = %q, want %q", sshRes.Remediation, expectedRem)
		}
	}

	// 2. Assert kptr_restrict failure & remediation
	kptrRes, ok := failedMap["cis-1.5.1-kernel-kptr-restrict"]
	if !ok {
		t.Errorf("expected cis-1.5.1-kernel-kptr-restrict to fail")
	} else {
		expectedRem := "Set kernel.kptr_restrict = 1 in /etc/sysctl.d/99-virto-hardening.conf and apply with sysctl --system."
		if kptrRes.Remediation != expectedRem {
			t.Errorf("kptr_restrict remediation = %q, want %q", kptrRes.Remediation, expectedRem)
		}
	}

	// 3. Assert sVirt security driver failure & remediation
	svirtRes, ok := failedMap["stig-svirt-mac-isolation"]
	if !ok {
		t.Errorf("expected stig-svirt-mac-isolation to fail")
	} else {
		expectedRem := `Configure sVirt MAC isolation: enable AppArmor or SELinux and set security_driver = "apparmor" in /etc/libvirt/qemu.conf, then restart libvirtd.`
		if svirtRes.Remediation != expectedRem {
			t.Errorf("sVirt remediation = %q, want %q", svirtRes.Remediation, expectedRem)
		}
	}

	// 4. Assert HostFirewall failure & remediation
	fwRes, ok := failedMap["cis-3.5.1-host-firewall"]
	if !ok {
		t.Errorf("expected cis-3.5.1-host-firewall to fail")
	} else {
		expectedRem := "Enable host firewall: ufw enable or systemctl enable --now nftables."
		if fwRes.Remediation != expectedRem {
			t.Errorf("HostFirewall remediation = %q, want %q", fwRes.Remediation, expectedRem)
		}
	}
}

// TestScannerCaching: assert consecutive Scan calls within TTL return cached result unless forced.
func TestScannerCaching(t *testing.T) {
	ctx := context.Background()
	root := tree(t, hardenedFiles())
	reader := NewReader(root)
	scanner := NewScanner(reader)

	// First scan executes checks and sets cache
	res1 := scanner.Scan(ctx, "host-cache", "node-1")
	if res1.TotalCount == 0 {
		t.Fatalf("first scan returned 0 checks")
	}

	// Cached() must return the cached report
	cached, ok := scanner.Cached()
	if !ok {
		t.Fatalf("Cached() reported false immediately after Scan")
	}
	if !cached.EvaluatedAt.Equal(res1.EvaluatedAt) {
		t.Errorf("Cached().EvaluatedAt = %v, want %v", cached.EvaluatedAt, res1.EvaluatedAt)
	}

	// Subsequent scan within TTL must return identical cached result
	time.Sleep(5 * time.Millisecond)
	res2 := scanner.Scan(ctx, "host-cache", "node-1")
	if !res2.EvaluatedAt.Equal(res1.EvaluatedAt) {
		t.Errorf("second scan evaluated at %v, expected cached timestamp %v", res2.EvaluatedAt, res1.EvaluatedAt)
	}

	// Forced scan must bypass cache and re-evaluate
	time.Sleep(5 * time.Millisecond)
	res3 := scanner.Scan(ctx, "host-cache", "node-1", true)
	if !res3.EvaluatedAt.After(res1.EvaluatedAt) {
		t.Errorf("forced scan evaluated at %v, expected strictly after %v", res3.EvaluatedAt, res1.EvaluatedAt)
	}

	// ScanForced helper also bypasses cache
	time.Sleep(5 * time.Millisecond)
	res4 := scanner.ScanForced(ctx, "host-cache", "node-1")
	if !res4.EvaluatedAt.After(res3.EvaluatedAt) {
		t.Errorf("ScanForced evaluated at %v, expected strictly after %v", res4.EvaluatedAt, res3.EvaluatedAt)
	}

	// Expired cache (> 60s) returns false from Cached()
	scanner.mu.Lock()
	scanner.lastScan = time.Now().Add(-65 * time.Second)
	scanner.mu.Unlock()

	if _, ok := scanner.Cached(); ok {
		t.Errorf("Cached() returned true for scan older than 60s")
	}
}

func TestCheckSSHVariations(t *testing.T) {
	ctx := context.Background()

	// Drop-in directory priority
	t.Run("DropInPriority", func(t *testing.T) {
		root := tree(t, map[string]string{
			"etc/ssh/sshd_config": "PermitRootLogin yes\nPermitEmptyPasswords yes",
			"etc/ssh/sshd_config.d/01-override.conf": "PermitRootLogin no\nPermitEmptyPasswords no",
		})
		r := NewReader(root)
		resRoot := CheckSSHRootLogin(ctx, r)
		if resRoot.Status != StatusPass {
			t.Errorf("CheckSSHRootLogin got %s, want pass", resRoot.Status)
		}
		resEmpty := CheckSSHEmptyPasswords(ctx, r)
		if resEmpty.Status != StatusPass {
			t.Errorf("CheckSSHEmptyPasswords got %s, want pass", resEmpty.Status)
		}
	})

	// Missing PermitRootLogin fails
	t.Run("MissingPermitRootLogin", func(t *testing.T) {
		root := tree(t, map[string]string{
			"etc/ssh/sshd_config": "Port 22",
		})
		r := NewReader(root)
		res := CheckSSHRootLogin(ctx, r)
		if res.Status != StatusFail {
			t.Errorf("missing PermitRootLogin got %s, want fail", res.Status)
		}
	})

	// Missing PermitEmptyPasswords passes (defaults to no)
	t.Run("MissingPermitEmptyPasswords", func(t *testing.T) {
		root := tree(t, map[string]string{
			"etc/ssh/sshd_config": "Port 22",
		})
		r := NewReader(root)
		res := CheckSSHEmptyPasswords(ctx, r)
		if res.Status != StatusPass {
			t.Errorf("missing PermitEmptyPasswords got %s, want pass", res.Status)
		}
	})
}

func TestCheckLibvirtSecurityDriverSELinux(t *testing.T) {
	ctx := context.Background()

	t.Run("SELinuxEnforcing", func(t *testing.T) {
		root := tree(t, map[string]string{
			"etc/libvirt/qemu.conf": `security_driver = ["selinux"]`,
			"sys/fs/selinux/enforce": "1",
		})
		r := NewReader(root)
		res := CheckLibvirtSecurityDriver(ctx, r)
		if res.Status != StatusPass {
			t.Errorf("got %s, want pass", res.Status)
		}
	})

	t.Run("SELinuxPermissive", func(t *testing.T) {
		root := tree(t, map[string]string{
			"etc/libvirt/qemu.conf": `security_driver = "selinux"`,
			"sys/fs/selinux/enforce": "0",
		})
		r := NewReader(root)
		res := CheckLibvirtSecurityDriver(ctx, r)
		if res.Status != StatusFail {
			t.Errorf("got %s, want fail for permissive SELinux", res.Status)
		}
	})
}

func TestCheckUnneededServices(t *testing.T) {
	ctx := context.Background()

	t.Run("InetdInsecure", func(t *testing.T) {
		root := tree(t, map[string]string{
			"etc/inetd.conf": "telnet stream tcp nowait root /usr/sbin/tcpd in.telnetd",
		})
		r := NewReader(root)
		res := CheckUnneededServices(ctx, r)
		if res.Status != StatusFail {
			t.Errorf("got %s, want fail", res.Status)
		}
		if !strings.Contains(res.Detail, "telnet") {
			t.Errorf("detail %q should mention telnet", res.Detail)
		}
	})

	t.Run("XinetdInsecure", func(t *testing.T) {
		root := tree(t, map[string]string{
			"etc/xinetd.d/tftp": "service tftp\n{\n  socket_type = dgram\n  disable = no\n}",
		})
		r := NewReader(root)
		res := CheckUnneededServices(ctx, r)
		if res.Status != StatusFail {
			t.Errorf("got %s, want fail", res.Status)
		}
	})

	t.Run("SystemdUnitInsecure", func(t *testing.T) {
		root := tree(t, map[string]string{
			"etc/systemd/system/multi-user.target.wants/vsftpd.service": "# vsftpd",
		})
		r := NewReader(root)
		res := CheckUnneededServices(ctx, r)
		if res.Status != StatusFail {
			t.Errorf("got %s, want fail", res.Status)
		}
	})
}

func TestCheckCoreDumpsRestricted(t *testing.T) {
	ctx := context.Background()

	t.Run("SuidDumpable2WithLimits", func(t *testing.T) {
		root := tree(t, map[string]string{
			"proc/sys/fs/suid_dumpable": "2",
			"etc/security/limits.conf": "* hard core 0",
		})
		r := NewReader(root)
		res := CheckCoreDumpsRestricted(ctx, r)
		if res.Status != StatusPass {
			t.Errorf("got %s, want pass", res.Status)
		}
	})

	t.Run("MissingLimits", func(t *testing.T) {
		root := tree(t, map[string]string{
			"proc/sys/fs/suid_dumpable": "0",
			"etc/security/limits.conf": "* soft nofile 65536",
		})
		r := NewReader(root)
		res := CheckCoreDumpsRestricted(ctx, r)
		if res.Status != StatusFail {
			t.Errorf("got %s, want fail", res.Status)
		}
	})
}
