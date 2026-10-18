package zfsrep

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"
)

// fakeZFS is ZFS on this host ("") and on ssh targets: dataset -> snapshots, oldest first.
type fakeZFS struct {
	mu       sync.Mutex
	hosts    map[string]map[string][]string
	failPipe error
	pipes    [][]string
}

func newFake() *fakeZFS {
	return &fakeZFS{hosts: map[string]map[string][]string{"": {"tank/vms/web": nil}, "root@backup": {}}}
}

// split turns a command into the host it runs on and the zfs arguments.
func split(name string, args []string) (string, []string) {
	if name == "ssh" {
		i := slices.Index(args, "--")
		return args[i-1], args[i+2:] // skip "zfs"
	}
	return "", args
}

func (f *fakeZFS) Run(_ context.Context, name string, args ...string) ([]byte, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	host, a := split(name, args)
	sets := f.hosts[host]
	switch a[0] {
	case "list":
		ds := a[len(a)-1]
		snaps, ok := sets[ds]
		if !ok {
			return nil, fmt.Errorf("cannot open '%s': dataset does not exist", ds)
		}
		var b strings.Builder
		for _, s := range snaps {
			fmt.Fprintf(&b, "%s@%s\n", ds, s)
		}
		return []byte(b.String()), nil
	case "snapshot":
		ds, snap, _ := strings.Cut(a[1], "@")
		sets[ds] = append(sets[ds], snap)
	case "destroy":
		ds, snap, _ := strings.Cut(a[1], "@")
		sets[ds] = slices.DeleteFunc(sets[ds], func(s string) bool { return s == snap })
	}
	return nil, nil
}

func (f *fakeZFS) Pipe(_ context.Context, send, recv []string) (uint64, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.pipes = append(f.pipes, append(slices.Clone(send), recv...))
	if f.failPipe != nil {
		return 10, f.failPipe
	}
	host, r := split(recv[0], recv[1:])
	dst := r[len(r)-1]
	_, snap, _ := strings.Cut(send[len(send)-1], "@")
	if send[2] == "-i" {
		base := strings.TrimPrefix(send[3], "@")
		if !slices.Contains(f.hosts[host][dst], base) {
			return 0, errors.New("incremental source does not exist on the target")
		}
	} else if _, ok := f.hosts[host][dst]; ok {
		return 0, errors.New("destination exists")
	}
	f.hosts[host][dst] = append(f.hosts[host][dst], snap)
	return 1000, nil
}

func open(t *testing.T, f *fakeZFS) (*Manager, *time.Time) {
	t.Helper()
	m, err := Open(filepath.Join(t.TempDir(), "zfs-replication.json"), f, nil)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 10, 18, 12, 0, 0, 0, time.UTC)
	m.now = func() time.Time { return now }
	return m, &now
}

func syncOnce(t *testing.T, m *Manager, id string) Status {
	t.Helper()
	if _, err := m.Start(id); err != nil {
		t.Fatal(err)
	}
	m.Wait()
	for _, s := range m.List() {
		if s.Spec.ID == id {
			return s
		}
	}
	t.Fatal("replication gone")
	return Status{}
}

func TestReplicationSendsFullThenIncrementalAndPrunes(t *testing.T) {
	f := newFake()
	m, now := open(t, f)
	st, err := m.Put(Spec{SourcePool: "tank/vms", SourceVolume: "web", TargetHost: "root@backup", TargetPool: "dr", IntervalMinutes: 5, Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	id := st.Spec.ID

	first := syncOnce(t, m, id)
	if first.State != "idle" || first.Error != "" || first.BytesTransferred != 1000 || first.LastSnapshot != "virtomir-rep-20261018-120000" {
		t.Fatalf("first pass: %+v", first)
	}
	if p := f.pipes[0]; slices.Contains(p, "-i") || slices.Contains(p, "-F") {
		t.Errorf("first pass should be a full send without -F: %v", p)
	}

	for i := 1; i <= 4; i++ {
		*now = now.Add(5 * time.Minute)
		if s := syncOnce(t, m, id); s.State != "idle" {
			t.Fatalf("pass %d: %+v", i+1, s)
		}
	}
	if p := f.pipes[1]; !slices.Contains(p, "-i") || !slices.Contains(p, "@virtomir-rep-20261018-120000") || !slices.Contains(p, "-F") {
		t.Errorf("second pass should be incremental from the first snapshot: %v", p)
	}
	want := []string{"virtomir-rep-20261018-121000", "virtomir-rep-20261018-121500", "virtomir-rep-20261018-122000"}
	if got := f.hosts[""]["tank/vms/web"]; !slices.Equal(got, want) {
		t.Errorf("source snapshots = %v", got)
	}
	if got := f.hosts["root@backup"]["dr/web"]; !slices.Equal(got, want) {
		t.Errorf("target snapshots = %v", got)
	}

	// The schedule survives a restart.
	m2, err := Open(m.path, f, nil)
	if err != nil || len(m2.List()) != 1 || m2.List()[0].LastSnapshot != want[2] {
		t.Errorf("reopened: %+v, %v", m2.List(), err)
	}
}

func TestFailedSendRemovesItsSnapshot(t *testing.T) {
	f := newFake()
	m, _ := open(t, f)
	st, _ := m.Put(Spec{SourcePool: "tank/vms", SourceVolume: "web", TargetHost: "root@backup", TargetPool: "dr", IntervalMinutes: 5})
	f.failPipe = errors.New("Host key verification failed.")
	s := syncOnce(t, m, st.Spec.ID)
	if s.State != "failed" || !strings.Contains(s.Error, "Host key verification failed") {
		t.Errorf("status = %+v", s)
	}
	if got := f.hosts[""]["tank/vms/web"]; len(got) != 0 {
		t.Errorf("snapshot left behind: %v", got)
	}
}

func TestExistingTargetWithoutCommonSnapshotIsNotOverwritten(t *testing.T) {
	f := newFake()
	f.hosts["root@backup"]["dr/web"] = []string{"someone-elses"}
	m, _ := open(t, f)
	st, _ := m.Put(Spec{SourcePool: "tank/vms", SourceVolume: "web", TargetHost: "root@backup", TargetPool: "dr", IntervalMinutes: 5})
	s := syncOnce(t, m, st.Spec.ID)
	if s.State != "failed" || !strings.Contains(s.Error, "will not overwrite") || len(f.pipes) != 0 {
		t.Errorf("status = %+v, pipes = %v", s, f.pipes)
	}
}

func TestScheduleStartsDuePasses(t *testing.T) {
	f := newFake()
	m, now := open(t, f)
	st, _ := m.Put(Spec{SourcePool: "tank/vms", SourceVolume: "web", TargetHost: "root@backup", TargetPool: "dr", IntervalMinutes: 10, Enabled: true})
	m.startDue()
	m.Wait()
	*now = now.Add(5 * time.Minute)
	m.startDue() // not due yet
	m.Wait()
	*now = now.Add(5 * time.Minute)
	m.startDue()
	m.Wait()
	if len(f.pipes) != 2 {
		t.Errorf("%d passes, want 2 (%s)", len(f.pipes), st.Spec.ID)
	}
}

func TestSpecValidationKeepsShellOut(t *testing.T) {
	good := Spec{SourcePool: "tank/vms", SourceVolume: "web-disk0", TargetHost: "root@10.0.0.2", TargetPool: "dr", IntervalMinutes: 5}
	if err := good.validate(); err != nil {
		t.Fatal(err)
	}
	for _, bad := range []func(*Spec){
		func(s *Spec) { s.SourceVolume = "web; rm -rf /" },
		func(s *Spec) { s.TargetPool = "dr$(id)" },
		func(s *Spec) { s.TargetHost = "-oProxyCommand=touch /tmp/x" },
		func(s *Spec) { s.TargetHost = "host name" },
		func(s *Spec) { s.SourcePool = "/tank" },
		func(s *Spec) { s.SourcePool = "tank//vms" },
		func(s *Spec) { s.IntervalMinutes = 0 },
	} {
		s := good
		bad(&s)
		if err := s.validate(); !errors.Is(err, ErrInvalid) {
			t.Errorf("%+v accepted", s)
		}
	}
}
