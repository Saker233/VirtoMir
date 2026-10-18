package vmware

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"libvirt.org/go/libvirtxml"
)

// fake plays virt-v2v for a Converter without the binary: it records what was run,
// reads the password file (it must exist while the "binary" runs), writes canned
// output files into the pool, feeds onLine canned --machine-readable lines and then
// returns the configured error.
type fake struct {
	versionErr error             // "--version" fails (broken installation)
	err        error             // the conversion itself fails
	lines      []string          // canned --machine-readable stdout
	write      map[string]string // files virt-v2v "wrote" into the pool
	pw         string            // what the password file held
	pwPath     string
	args       []string
	env        []string
}

func (f *fake) exec(ctx context.Context, env []string, args []string, onLine func(string)) ([]byte, error) {
	f.args, f.env = args, env
	if slices.Equal(args, []string{"--version"}) {
		return []byte("virt-v2v 2.4.0\n"), f.versionErr
	}
	pool := ""
	if i := slices.Index(args, "-os"); i >= 0 {
		pool = args[i+1]
	}
	if i := slices.Index(args, "--password-file"); i >= 0 {
		f.pwPath = args[i+1]
		b, err := os.ReadFile(f.pwPath)
		if err != nil {
			return nil, fmt.Errorf("the password file is missing: %v", err)
		}
		f.pw = string(b)
	}
	if pool != "" {
		for name, content := range f.write {
			if err := os.WriteFile(filepath.Join(pool, name), []byte(content), 0o600); err != nil {
				return nil, err
			}
		}
	}
	for _, l := range f.lines {
		if onLine != nil {
			onLine(l)
		}
	}
	return nil, f.err
}

func foundLook(string) (string, error)   { return "/usr/bin/virt-v2v", nil }
func missingLook(string) (string, error) { return "", exec.ErrNotFound }

var vCenterConn = Connection{URL: "https://vcenter.example.com", Username: "administrator@vsphere.local", Password: "hunter2"}

var testVM = VM{
	ID: "vm-42", Name: "web01", Datacenter: "DC0", Host: "DC0_H0", PowerState: "off",
	VCPUs: 2, MemoryMiB: 4096, GuestOS: "Ubuntu Linux (64-bit)",
	Disks: []VMDisk{{Datastore: "datastore1", CapacityBytes: 20 << 30}},
	NICs:  []VMNIC{{Network: "VM Network", MAC: "00:0c:29:aa:bb:cc"}},
}

// v2vXML is what virt-v2v -o local writes: the guest's name, disks relative to -os.
const v2vXML = `<domain type='kvm'>
  <name>web01</name>
  <memory unit='MiB'>4096</memory>
  <vcpu>2</vcpu>
  <os><type arch='x86_64'>hvm</type></os>
  <devices>
    <disk type='file' device='disk'>
      <driver name='qemu' type='qcow2'/>
      <source file='web01-disk0'/>
      <target dev='vda' bus='virtio'/>
    </disk>
    <disk type='file' device='disk'>
      <driver name='qemu' type='qcow2'/>
      <source file='web01-disk1'/>
      <target dev='vdb' bus='virtio'/>
    </disk>
    <interface type='network'>
      <source network='default'/>
    </interface>
    <graphics type='vnc'/>
  </devices>
</domain>`

// The command line: the shape of the virt-v2v call, and the password nowhere on it.
// Definition fails only at the adoption step (no libvirt client in tests).
func TestConvertCommand(t *testing.T) {
	f := &fake{write: map[string]string{"web01.xml": v2vXML, "web01-disk0": "d0", "web01-disk1": "d1"}}
	conv := &Converter{exec: f.exec, look: foundLook}
	pool := t.TempDir()
	_, err := conv.Convert(context.Background(), vCenterConn, testVM, ConvertOpts{
		PoolPath:   pool,
		NetworkMap: map[string]string{"VM Network": "default", "DMZ": "isolated"},
		Name:       "web01-migrated",
		VirtioISO:  "/srv/virtio-win.iso",
	}, func(string) {})
	if !errors.Is(err, ErrRefused) {
		t.Fatalf("Convert without a libvirt client = %v, want the refused adoption", err)
	}

	if f.env != nil {
		t.Errorf("env = %v, want none: the password never travels the environment", f.env)
	}
	i := slices.Index(f.args, "--password-file")
	if i < 0 || i+1 >= len(f.args) {
		t.Fatalf("no --password-file argument: %v", f.args)
	}
	if f.pw != "hunter2\n" {
		t.Errorf("the password file held %q", f.pw)
	}
	if _, err := os.Stat(f.pwPath); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("the password file %s still exists after Convert returned", f.pwPath)
	}
	for _, a := range f.args {
		if strings.Contains(a, "hunter2") {
			t.Errorf("the password is on the command line: %q", a)
		}
	}
	if strings.Contains(err.Error(), "hunter2") {
		t.Errorf("the password is in the returned error: %v", err)
	}

	want := []string{
		"-ic", "vpx://administrator%40vsphere.local@vcenter.example.com/DC0/DC0_H0?no_verify=1",
		"--password-file", f.args[i+1],
		"-o", "local",
		"-os", pool,
		"--machine-readable",
		"--network", "DMZ:isolated",
		"--network", "VM Network:default",
		"--inject-drivers", "/srv/virtio-win.iso",
		"web01",
	}
	if !slices.Equal(f.args, want) {
		t.Errorf("args\n got %v\nwant %v", f.args, want)
	}
}

// A standalone ESXi connection talks to the host itself, not through vCenter.
func TestConvertESXiInput(t *testing.T) {
	f := &fake{write: map[string]string{"web01.xml": v2vXML}}
	conv := &Converter{exec: f.exec, look: foundLook}
	c := Connection{URL: "https://esxi.example.com", Username: "root", Password: "p", ESXi: true}
	_, err := conv.Convert(context.Background(), c, testVM, ConvertOpts{PoolPath: t.TempDir()}, nil)
	if !errors.Is(err, ErrRefused) { // fails at the adoption step, not before
		t.Fatalf("Convert = %v", err)
	}
	if i := slices.Index(f.args, "-ic"); i < 0 || f.args[i+1] != "esx://root@esxi.example.com?no_verify=1" {
		t.Errorf("-ic in %v, want esx://root@esxi.example.com?no_verify=1", f.args)
	}
}

func TestInputURLNeedsDatacenter(t *testing.T) {
	if _, err := inputURL(vCenterConn, VM{Name: "web01", Host: "DC0_H0"}); !errors.Is(err, ErrInvalid) {
		t.Errorf("no datacenter = %v, want ErrInvalid", err)
	}
	if _, err := inputURL(vCenterConn, VM{Name: "web01", Datacenter: "DC0"}); !errors.Is(err, ErrInvalid) {
		t.Errorf("no host = %v, want ErrInvalid", err)
	}
}

// --machine-readable lines become report() lines, in order, as they come.
func TestConvertProgressReported(t *testing.T) {
	f := &fake{
		write: map[string]string{"web01.xml": v2vXML, "web01-disk0": "d0", "web01-disk1": "d1"},
		lines: []string{
			`{"type":"message","value":"Opening the VMware source"}`,
			`{"type":"progress","disk":1,"disks":2,"per_cent":45}`,
			`{"type":"progress","disk":1,"disks":2,"per_cent":46}`,
			`{"type":"progress","disk":2,"disks":2,"per_cent":10}`,
			`not JSON at all`,
			`{"type":"warning","value":"no virtio drivers found"}`,
		},
	}
	var got []string
	conv := &Converter{exec: f.exec, look: foundLook}
	_, err := conv.Convert(context.Background(), vCenterConn, testVM, ConvertOpts{PoolPath: t.TempDir()}, func(l string) { got = append(got, l) })
	if !errors.Is(err, ErrRefused) { // fails at the adoption step, not before
		t.Fatalf("Convert = %v", err)
	}
	want := []string{
		"Converting web01 from VMware…",
		"Opening the VMware source",
		"Converting disk 1 of 2… 45 %",
		"Converting disk 1 of 2… 46 %",
		"Converting disk 2 of 2… 10 %",
		"virt-v2v warning: no virtio drivers found",
	}
	if !slices.Equal(got, want) {
		t.Errorf("reports\n got %v\nwant %v", got, want)
	}
}

// A Windows guest without a provided driver ISO gets a note, not a failure.
func TestConvertWindowsNote(t *testing.T) {
	f := &fake{write: map[string]string{"win01.xml": v2vXML}}
	vm := testVM
	vm.Name, vm.GuestOS = "win01", "Microsoft Windows Server 2019"
	var got []string
	conv := &Converter{exec: f.exec, look: foundLook}
	if _, err := conv.Convert(context.Background(), vCenterConn, vm, ConvertOpts{PoolPath: t.TempDir()}, func(l string) { got = append(got, l) }); !errors.Is(err, ErrRefused) {
		t.Fatalf("Convert = %v", err)
	}
	if len(got) < 2 || !strings.HasPrefix(got[1], "No virtio driver ISO was provided") {
		t.Errorf("no virtio note in %v", got)
	}
}

// A non-zero exit is classed like libvirt errors are, and never carries the password.
func TestConvertFailureClassified(t *testing.T) {
	for _, tc := range []struct {
		err    error
		want   error
		masked bool
	}{
		{errors.New("virt-v2v: could not connect to vcenter.example.com: connection refused"), ErrRefused, false},
		{errors.New("virt-v2v: authentication failed"), ErrRefused, false},
		{errors.New("virt-v2v: VM 'web01' not found"), ErrInvalid, false},
		{errors.New("virt-v2v: login with password 'hunter2' failed: connection refused"), ErrRefused, true},
	} {
		f := &fake{err: tc.err, write: map[string]string{"web01-disk0": "partial"}}
		conv := &Converter{exec: f.exec, look: foundLook}
		pool := t.TempDir()
		if err := os.WriteFile(filepath.Join(pool, "keep.qcow2"), []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
		_, err := conv.Convert(context.Background(), vCenterConn, testVM, ConvertOpts{PoolPath: pool}, nil)
		if !errors.Is(err, tc.want) {
			t.Errorf("Convert(%v) = %v, want class %v", tc.err, err, tc.want)
		}
		if strings.Contains(err.Error(), "hunter2") {
			t.Errorf("the password leaked into %q", err.Error())
		}
		if tc.masked && !strings.Contains(err.Error(), Secret) {
			t.Errorf("the password was not masked in %q", err.Error())
		}
		// partial output is gone, an unrelated file stays
		if _, err := os.Stat(filepath.Join(pool, "web01-disk0")); !errors.Is(err, os.ErrNotExist) {
			t.Errorf("the partial disk was left behind")
		}
		if _, err := os.Stat(filepath.Join(pool, "keep.qcow2")); err != nil {
			t.Errorf("an unrelated file was removed: %v", err)
		}
	}
}

// A failure after full output (here: the definition cannot be adopted) removes the
// disks the definition names and the definition itself.
func TestConvertCleanupAfterPartialOutput(t *testing.T) {
	f := &fake{write: map[string]string{"web01.xml": v2vXML, "web01-disk0": "d0", "web01-disk1": "d1"}}
	conv := &Converter{exec: f.exec, look: foundLook} // no libvirt client: adoption fails
	pool := t.TempDir()
	if _, err := conv.Convert(context.Background(), vCenterConn, testVM, ConvertOpts{PoolPath: pool, Name: "web01-migrated"}, nil); !errors.Is(err, ErrRefused) {
		t.Fatalf("Convert = %v, want the refused adoption", err)
	}
	for _, name := range []string{"web01.xml", "web01-disk0", "web01-disk1"} {
		if _, err := os.Stat(filepath.Join(pool, name)); !errors.Is(err, os.ErrNotExist) {
			t.Errorf("%s was left behind", name)
		}
	}
}

func TestConvertRefusesBadRequests(t *testing.T) {
	conv := &Converter{exec: (&fake{}).exec, look: foundLook}
	if _, err := conv.Convert(context.Background(), Connection{}, testVM, ConvertOpts{PoolPath: t.TempDir()}, nil); !errors.Is(err, ErrRefused) {
		t.Errorf("no connection = %v, want ErrRefused", err)
	}
	if _, err := conv.Convert(context.Background(), vCenterConn, testVM, ConvertOpts{}, nil); !errors.Is(err, ErrInvalid) {
		t.Errorf("no pool = %v, want ErrInvalid", err)
	}
	if _, err := conv.Convert(context.Background(), vCenterConn, testVM, ConvertOpts{PoolPath: filepath.Join(t.TempDir(), "nope")}, nil); !errors.Is(err, ErrInvalid) {
		t.Errorf("missing pool directory = %v, want ErrInvalid", err)
	}
}

func TestPreflight(t *testing.T) {
	ctx := context.Background()

	// everything good
	conv := &Converter{exec: (&fake{}).exec, look: foundLook}
	if err := conv.Preflight(ctx, vCenterConn, testVM, 30<<30); err != nil {
		t.Fatalf("a good pre-flight = %v, want nil", err)
	}

	// every problem at once: binary missing, VM running, pool too small
	conv = &Converter{exec: (&fake{}).exec, look: missingLook}
	vm := testVM
	vm.PowerState = "on"
	err := conv.Preflight(ctx, vCenterConn, vm, 1<<30)
	var iss Issues
	if !errors.As(err, &iss) {
		t.Fatalf("Preflight = %v, want Issues", err)
	}
	if len(iss) != 3 {
		t.Fatalf("Preflight found %d issues (%v), want 3", len(iss), iss)
	}
	if !errors.Is(err, ErrInvalid) {
		t.Errorf("Preflight class = %v, want ErrInvalid", err)
	}
	for _, want := range []string{
		"virt-v2v is not installed",
		"Shut it down there first — warm or snapshot migration is not offered",
		"The disks of web01 need 22.0 GiB (with 10% headroom), but the pool has only 1.0 GiB free",
	} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("issue %q missing from %v", want, iss)
		}
	}
	if !strings.Contains(err.Error(), "; ") {
		t.Errorf("Issues.Error() does not join: %v", err)
	}

	// the binary alone: one clear issue
	conv = &Converter{exec: (&fake{}).exec, look: missingLook}
	if err := conv.Preflight(ctx, vCenterConn, testVM, 30<<30); !errors.As(err, &iss) || len(iss) != 1 || !strings.Contains(iss[0], "apt install virt-v2v") {
		t.Errorf("not installed = %v, want the one installation issue", err)
	}

	// installed but broken
	conv = &Converter{exec: (&fake{versionErr: errors.New("libguestfs is missing\nsecond line")}).exec, look: foundLook}
	if err := conv.Preflight(ctx, vCenterConn, testVM, 30<<30); !errors.As(err, &iss) || len(iss) != 1 || !strings.Contains(iss[0], "virt-v2v is installed but does not run: libguestfs is missing") {
		t.Errorf("broken binary = %v, want the one run issue", err)
	}

	// no disks is a problem too
	vm = testVM
	vm.Disks = nil
	conv = &Converter{exec: (&fake{}).exec, look: foundLook}
	if err := conv.Preflight(ctx, vCenterConn, vm, 30<<30); !errors.As(err, &iss) || !strings.Contains(iss[0], "no disks") {
		t.Errorf("diskless VM = %v, want the no-disks issue", err)
	}
}

func TestPrepareDefinition(t *testing.T) {
	out, disks, err := prepareDefinition([]byte(v2vXML), "/pool", "web01-migrated")
	if err != nil {
		t.Fatal(err)
	}
	var x libvirtxml.Domain
	if err := x.Unmarshal(out); err != nil {
		t.Fatal(err)
	}
	if x.Name != "web01-migrated" {
		t.Errorf("name = %q, want web01-migrated", x.Name)
	}
	want := []string{filepath.Join("/pool", "web01-disk0"), filepath.Join("/pool", "web01-disk1")}
	if !slices.Equal(disks, want) {
		t.Errorf("disks = %v, want %v", disks, want)
	}
	for i, d := range x.Devices.Disks {
		if d.Source.File.File != want[i] {
			t.Errorf("disk %d points at %q, want %q", i, d.Source.File.File, want[i])
		}
	}
	if vnc := x.Devices.Graphics[0].VNC; vnc == nil || vnc.Listen != "127.0.0.1" || vnc.AutoPort != "yes" || vnc.Port != -1 {
		t.Errorf("the console is not local-only: %+v", vnc)
	}
	if _, _, err := prepareDefinition([]byte("not <valid xml"), "/pool", "x"); !errors.Is(err, ErrInvalid) {
		t.Errorf("garbage definition = %v, want ErrInvalid", err)
	}
}

// Realistic failure output: a libvirt info line first, then 'virt-v2v: error: ...'.
func TestCleanV2VErrorRealistic(t *testing.T) {
	stderr := "2026-10-19 01:23:45.123+0000: 12345: info : libvirt version: 11.3.0, package: 1ubuntu1\n" +
		"2026-10-19 01:23:45.124+0000: 12345: info : hostname: dev-host\n" +
		"virt-v2v: error: inspection failed: no operating system found on this disk"
	got := cleanV2VError(stderr, "exit status 1")
	want := "virt-v2v: error: inspection failed: no operating system found on this disk"
	if got != want {
		t.Errorf("cleanV2VError = %q, want %q", got, want)
	}
	if strings.Contains(got, "libvirt version") || strings.Contains(got, "info :") {
		t.Errorf("cleanV2VError leaked libvirt info noise: %q", got)
	}
}

// Stripping various timestamps and prefixes from error lines.
func TestCleanV2VErrorTimestamps(t *testing.T) {
	for _, tc := range []struct {
		name  string
		input string
		want  string
	}{
		{
			name:  "libvirt timestamp with pid",
			input: "2026-10-19 01:23:45.123+0000: 12345: info : libvirt version: 11.3.0\n2026-10-19 01:23:46.456+0000: 12345: virt-v2v: error: disk conversion failed",
			want:  "virt-v2v: error: disk conversion failed",
		},
		{
			name:  "bracketed timestamp",
			input: "[   0.0] info : libvirt version: 11.3.0\n[   1.2] virt-v2v: error: disk conversion failed",
			want:  "virt-v2v: error: disk conversion failed",
		},
		{
			name:  "simple iso timestamp",
			input: "2026-10-19 01:23:45 virt-v2v: error: disk conversion failed",
			want:  "virt-v2v: error: disk conversion failed",
		},
		{
			name:  "timestamp on non-virt-v2v prefixed error",
			input: "2026-10-19 01:23:45.123+0000: 12345: error : virNetSocketReadWire: connection reset",
			want:  "virt-v2v: error : virNetSocketReadWire: connection reset",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := cleanV2VError(tc.input, "exit status 1")
			if got != tc.want {
				t.Errorf("got %q, want %q", got, tc.want)
			}
		})
	}
}

// Multiple error lines are all preserved when present.
func TestCleanV2VErrorMultipleErrors(t *testing.T) {
	stderr := "2026-10-19 01:23:45.123+0000: 12345: info : libvirt version: 11.3.0\n" +
		"virt-v2v: error: could not connect to vcenter.example.com\n" +
		"virt-v2v: error: connection refused"
	got := cleanV2VError(stderr, "exit status 1")
	want := "virt-v2v: error: could not connect to vcenter.example.com\nvirt-v2v: error: connection refused"
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

// When no line contains 'error', the last 5 non-empty non-noise lines are returned.
func TestCleanV2VErrorNoExplicitErrorFallback(t *testing.T) {
	stderr := "2026-10-19 01:23:45.123+0000: 12345: info : libvirt version: 11.3.0\n" +
		"step 1: connect\n" +
		"step 2: query\n" +
		"step 3: open disk\n" +
		"step 4: convert\n" +
		"step 5: copy blocks\n" +
		"step 6: finalize\n" +
		"step 7: crash"
	got := cleanV2VError(stderr, "exit status 1")
	want := "virt-v2v: step 3: open disk\nstep 4: convert\nstep 5: copy blocks\nstep 6: finalize\nstep 7: crash"
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

// Error output is capped at 1000 characters.
func TestCleanV2VErrorCap1000(t *testing.T) {
	longErrMsg := "virt-v2v: error: " + strings.Repeat("x", 2000)
	got := cleanV2VError(longErrMsg, "fallback")
	if len([]rune(got)) > 1000 {
		t.Errorf("cleanV2VError length = %d runes, want <= 1000", len([]rune(got)))
	}
	if len([]rune(got)) != 1000 {
		t.Errorf("cleanV2VError length = %d runes, want 1000", len([]rune(got)))
	}
	if !strings.HasPrefix(got, "virt-v2v: error: xxx") {
		t.Errorf("unexpected prefix: %q", got[:30])
	}
}

// Empty stderr falls back to the provided fallback string.
func TestCleanV2VErrorEmptyFallback(t *testing.T) {
	got := cleanV2VError("", "exit status 1")
	if got != "virt-v2v: exit status 1" {
		t.Errorf("got %q, want %q", got, "virt-v2v: exit status 1")
	}

	// Stderr with only info lines also falls back.
	infoOnly := "2026-10-19 01:23:45.123+0000: 12345: info : libvirt version: 11.3.0\n" +
		"2026-10-19 01:23:45.124+0000: 12345: info : hostname: host1"
	got = cleanV2VError(infoOnly, "exit status 1")
	if got != "virt-v2v: exit status 1" {
		t.Errorf("got %q, want %q", got, "virt-v2v: exit status 1")
	}
}

// End-to-end Convert with realistic failure output passes the classified meaningful error.
func TestConvertWithRealisticV2VFailure(t *testing.T) {
	realisticStderr := "2026-10-19 01:23:45.123+0000: 12345: info : libvirt version: 11.3.0, package: 1ubuntu1\n" +
		"2026-10-19 01:23:45.124+0000: 12345: info : hostname: dev-host\n" +
		"virt-v2v: error: authentication failed: invalid credentials"
	errStr := cleanV2VError(realisticStderr, "exit status 1")
	f := &fake{err: errors.New(errStr)}
	conv := &Converter{exec: f.exec, look: foundLook}
	_, err := conv.Convert(context.Background(), vCenterConn, testVM, ConvertOpts{PoolPath: t.TempDir()}, nil)
	if !errors.Is(err, ErrRefused) {
		t.Fatalf("Convert = %v, want ErrRefused", err)
	}
	if strings.Contains(err.Error(), "libvirt version") {
		t.Errorf("error contains libvirt info noise: %v", err)
	}
	if !strings.Contains(err.Error(), "virt-v2v: error: authentication failed: invalid credentials") {
		t.Errorf("error missing meaningful failure: %v", err)
	}
	if !strings.Contains(err.Error(), "check the VMware user and password") {
		t.Errorf("error missing classification hint: %v", err)
	}
}
